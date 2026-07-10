package hashistack

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	napi "github.com/hashicorp/nomad/api"
)

// Nomad wraps the Nomad API for the workspace operations the portal performs:
// create the dynamic host volume, parse+register the rendered job, list a
// developer's workspace jobs, and enumerate host ports already in use.
type Nomad struct {
	c *napi.Client
}

func NewNomad(addr, token string, tls TLSOptions) (*Nomad, error) {
	cfg := napi.DefaultConfig()
	cfg.Address = addr
	cfg.SecretID = token
	cfg.TLSConfig = &napi.TLSConfig{CACert: tls.CACertPath, Insecure: tls.SkipVerify}
	c, err := napi.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("nomad: new client: %w", err)
	}
	return &Nomad{c: c}, nil
}

// Ping checks Nomad is reachable (leader lookup), for readiness.
func (n *Nomad) Ping(ctx context.Context) error {
	if _, err := n.c.Status().Leader(); err != nil {
		return fmt.Errorf("nomad: leader: %w", err)
	}
	return nil
}

// CreateNamespace registers (upserts) a Nomad namespace. Idempotent by nature —
// Register overwrites an existing namespace of the same name.
func (n *Nomad) CreateNamespace(name, description string) error {
	_, err := n.c.Namespaces().Register(&napi.Namespace{Name: name, Description: description}, nil)
	if err != nil {
		return fmt.Errorf("nomad: register namespace %q: %w", name, err)
	}
	return nil
}

// DeleteNamespace removes a Nomad namespace. Every job in it must already be
// purged (Nomad refuses to delete a namespace with non-terminal jobs).
func (n *Nomad) DeleteNamespace(name string) error {
	if _, err := n.c.Namespaces().Delete(name, nil); err != nil {
		return fmt.Errorf("nomad: delete namespace %q: %w", name, err)
	}
	return nil
}

// ListJobIDs returns the IDs of every job registered in namespace.
func (n *Nomad) ListJobIDs(namespace string) ([]string, error) {
	stubs, _, err := n.c.Jobs().List(&napi.QueryOptions{Namespace: namespace})
	if err != nil {
		return nil, fmt.Errorf("nomad: list jobs in %q: %w", namespace, err)
	}
	ids := make([]string, 0, len(stubs))
	for _, s := range stubs {
		ids = append(ids, s.ID)
	}
	return ids, nil
}

// ListCSIVolumeNames returns the names of every CSI volume in namespace. Used by
// project delete to sweep workspace home volumes BEFORE the namespace is removed
// — Nomad happily deletes a namespace that still holds volumes, which then become
// undeletable orphans ("namespace does not exist") AND leak the backing EBS
// volumes in AWS.
func (n *Nomad) ListCSIVolumeNames(namespace string) ([]string, error) {
	vols, _, err := n.c.CSIVolumes().List(&napi.QueryOptions{Namespace: namespace})
	if err != nil {
		return nil, fmt.Errorf("nomad: list CSI volumes in %q: %w", namespace, err)
	}
	names := make([]string, 0, len(vols))
	for _, v := range vols {
		names = append(names, v.Name)
	}
	return names, nil
}

// DeleteACLPolicy removes an ACL policy by name.
func (n *Nomad) DeleteACLPolicy(name string) error {
	if _, err := n.c.ACLPolicies().Delete(name, nil); err != nil {
		return fmt.Errorf("nomad: delete acl policy %q: %w", name, err)
	}
	return nil
}

// DeleteBindingRulesForPolicy removes every OIDC binding rule that binds the
// given policy name (the inverse of CreateBindingRule). No-op when none match.
func (n *Nomad) DeleteBindingRulesForPolicy(bindName string) error {
	stubs, _, err := n.c.ACLBindingRules().List(nil)
	if err != nil {
		return fmt.Errorf("nomad: list binding rules: %w", err)
	}
	for _, s := range stubs {
		r, _, err := n.c.ACLBindingRules().Get(s.ID, nil)
		if err != nil {
			return fmt.Errorf("nomad: get binding rule %q: %w", s.ID, err)
		}
		if r.BindName == bindName {
			if _, err := n.c.ACLBindingRules().Delete(s.ID, nil); err != nil {
				return fmt.Errorf("nomad: delete binding rule %q: %w", s.ID, err)
			}
		}
	}
	return nil
}

// UpsertACLPolicy creates or replaces an ACL policy (upsert semantics).
func (n *Nomad) UpsertACLPolicy(name, description, rulesHCL string) error {
	_, err := n.c.ACLPolicies().Upsert(&napi.ACLPolicy{Name: name, Description: description, Rules: rulesHCL}, nil)
	if err != nil {
		return fmt.Errorf("nomad: upsert acl policy %q: %w", name, err)
	}
	return nil
}

// CreateBindingRule creates a policy binding rule on an OIDC auth method, binding
// the selector to bindName. Idempotent: an equivalent rule (same auth method,
// selector, and bind name) is left untouched rather than duplicated, since Nomad
// binding-rule IDs are server-generated and Create is not upsert.
func (n *Nomad) CreateBindingRule(authMethod, selector, bindName string) error {
	stubs, _, err := n.c.ACLBindingRules().List(nil)
	if err != nil {
		return fmt.Errorf("nomad: list binding rules: %w", err)
	}
	for _, s := range stubs {
		if s.AuthMethod != authMethod {
			continue
		}
		rule, _, err := n.c.ACLBindingRules().Get(s.ID, nil)
		if err != nil {
			return fmt.Errorf("nomad: get binding rule %s: %w", s.ID, err)
		}
		if rule.Selector == selector && rule.BindName == bindName {
			return nil // equivalent rule already present
		}
	}
	_, _, err = n.c.ACLBindingRules().Create(&napi.ACLBindingRule{
		AuthMethod: authMethod,
		Selector:   selector,
		BindType:   "policy",
		BindName:   bindName,
	}, nil)
	if err != nil {
		return fmt.Errorf("nomad: create binding rule (%s): %w", authMethod, err)
	}
	return nil
}

// CSI constants for the per-workspace durable EBS volume (AWS EBS CSI driver).
const (
	csiPluginID       = "aws-ebs"
	homeVolumeSizeGiB = 20
	// csiZoneTopologyKey is the AZ topology segment the AWS EBS CSI driver
	// advertises/consumes; a volume is constrained to a node's AZ (EBS volumes
	// are AZ-scoped) so it can only ever attach to clients in that zone.
	csiZoneTopologyKey = "topology.ebs.csi.aws.com/zone"
	// nodeAWSZoneAttr is the Nomad node fingerprint attribute holding the AWS AZ.
	nodeAWSZoneAttr = "platform.aws.placement.availability-zone"
	// workspaceVolumeBackupTag stamps every workspace EBS volume so the AWS Backup
	// plan can select them by tag (key=value form for tagSpecification).
	workspaceVolumeBackupTag = "backup=secured-workspace"
)

// CreateHostVolume provisions the persistent /home/dev volume as a durable AWS
// EBS volume via the EBS CSI driver (aws-ebs plugin), replacing the node-local
// mkdir host volume so workspace state survives node replacement / crash. The
// name and signature are unchanged so workspace/service.go create/destroy are
// untouched. The volume is constrained to the AZ of a ready node in the target
// pool (EBS volumes are AZ-scoped) so it can later reattach to any healthy client
// in that zone; on this single-AZ deployment that is always the one public
// subnet's AZ. A non-empty nodePool (e.g. "gpu") selects a node there; empty
// means the implicit "default" pool (the main node). readyNodeInPool also skips
// any stale "down" node a pooled destroy/reprovision cycle leaves behind.
func (n *Nomad) CreateHostVolume(namespace, name, nodePool string) error {
	pool := nodePool
	if pool == "" {
		pool = "default"
	}
	nodeID, err := n.readyNodeInPool(pool)
	if err != nil {
		return err
	}
	zone, err := n.nodeZone(nodeID)
	if err != nil {
		return err
	}
	sizeBytes := int64(homeVolumeSizeGiB) * 1024 * 1024 * 1024
	vol := &napi.CSIVolume{
		ID:        name,
		Name:      name,
		Namespace: namespace,
		PluginID:  csiPluginID,
		RequestedCapabilities: []*napi.CSIVolumeCapability{{
			AccessMode:     napi.CSIVolumeAccessModeSingleNodeWriter,
			AttachmentMode: napi.CSIVolumeAttachmentModeFilesystem,
		}},
		RequestedCapacityMin: sizeBytes,
		RequestedCapacityMax: sizeBytes,
		MountOptions:         &napi.CSIMountOptions{FSType: "ext4"},
		Parameters: map[string]string{
			"type":      "gp3",
			"encrypted": "true",
			// tagSpecification_N tags the backing EBS volume at create time (the
			// Nomad-native alternative to the k8s-only --extra-create-metadata):
			// a human-readable Name for the console, and a stable selector the
			// AWS Backup plan matches on.
			"tagSpecification_1": "Name=" + name,
			"tagSpecification_2": workspaceVolumeBackupTag,
		},
		RequestedTopologies: &napi.CSITopologyRequest{
			Required: []*napi.CSITopology{{
				Segments: map[string]string{csiZoneTopologyKey: zone},
			}},
		},
	}
	// Create both provisions the EBS volume through the controller AND registers
	// it with Nomad in one call. The controller round-trip means the volume may
	// not be flagged schedulable the instant Create returns, so wait for it before
	// the caller registers the job (else placement fails "missing compatible CSI").
	if _, _, err := n.c.CSIVolumes().Create(vol, &napi.WriteOptions{Namespace: namespace}); err != nil {
		return fmt.Errorf("nomad: create CSI volume %q: %w", name, err)
	}
	if err := n.waitCSIVolumeSchedulable(name, namespace); err != nil {
		return err
	}
	return nil
}

// nodeZone returns the AWS availability-zone of a node from its fingerprint, so a
// CSI volume can be constrained (topology) to the zone where it will attach.
func (n *Nomad) nodeZone(nodeID string) (string, error) {
	node, _, err := n.c.Nodes().Info(nodeID, nil)
	if err != nil {
		return "", fmt.Errorf("nomad: node info %q: %w", nodeID, err)
	}
	if z := node.Attributes[nodeAWSZoneAttr]; z != "" {
		return z, nil
	}
	return "", fmt.Errorf("nomad: node %q has no AWS availability-zone attribute", nodeID)
}

// waitCSIVolumeSchedulable polls a freshly-created CSI volume until Nomad marks
// it schedulable (all plugin-health fields green), so the workspace job is
// registered only once placement can actually claim it. Bounded: a stuck
// controller surfaces a clear error instead of hanging.
func (n *Nomad) waitCSIVolumeSchedulable(name, namespace string) error {
	qo := &napi.QueryOptions{Namespace: namespace}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		vol, _, err := n.c.CSIVolumes().Info(name, qo)
		if err == nil && vol.Schedulable {
			return nil
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("nomad: CSI volume %q not schedulable after 30s", name)
}

// readyNodeInPool returns the ID of a ready, eligible, non-draining client node
// in the given node pool. Used to pin a host volume to a live node so placement
// never lands on a stale "down" node (e.g. one left behind by a GPU-node
// destroy/reprovision before Nomad garbage-collects it), which the server can't
// reach ("No path to node").
func (n *Nomad) readyNodeInPool(pool string) (string, error) {
	nodes, _, err := n.c.Nodes().List(nil)
	if err != nil {
		return "", fmt.Errorf("nomad: list nodes: %w", err)
	}
	for _, node := range nodes {
		if node.NodePool == pool &&
			node.Status == napi.NodeStatusReady &&
			node.SchedulingEligibility == napi.NodeSchedulingEligible &&
			!node.Drain {
			return node.ID, nil
		}
	}
	return "", fmt.Errorf("nomad: no ready node in pool %q", pool)
}

// ResolvePlacementIP polls for the job's first allocation and returns the private
// IP of the node it landed on, so the Boundary target host points at the actual
// placement node (e.g. the GPU node) rather than the default main-node IP. Bounded:
// a job whose node-pool/GPU-device constraints can't be met never places, and this
// returns a clear error instead of guessing a host.
func (n *Nomad) ResolvePlacementIP(namespace, jobID string) (string, error) {
	ip, _, err := n.ResolvePlacement(namespace, jobID)
	return ip, err
}

// ResolvePlacement waits for the job's current allocation to be placed and returns
// the node's private IP plus the host port assigned to the "http" label. For a
// static port the assigned value equals the declared port; for a Nomad-assigned
// dynamic port (project-plane MCP jobs) it is the only way to learn the reachable
// host port for the ContextForge peer URL. port is 0 if the allocation exposes no
// "http" port.
//
// On a job UPDATE the list briefly contains both the stopping allocation and its
// replacement (in no useful order — the API returns them by ID), so the placement
// must be the newest run-desired allocation; picking the list head healed the
// gateway peer to the dying alloc's port (observed live: peer pinned to the old
// host port, deactivated by ContextForge after 3 failed health checks).
func (n *Nomad) ResolvePlacement(namespace, jobID string) (ip string, port int, err error) {
	qo := &napi.QueryOptions{Namespace: namespace}
	var alloc *napi.AllocationListStub
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		allocs, _, err := n.c.Jobs().Allocations(jobID, false, qo)
		if err != nil {
			return "", 0, fmt.Errorf("nomad: list allocations for %q: %w", jobID, err)
		}
		for _, a := range allocs {
			if a.DesiredStatus != "run" || a.NodeID == "" {
				continue
			}
			if alloc == nil || a.CreateIndex > alloc.CreateIndex {
				alloc = a
			}
		}
		if alloc != nil {
			break
		}
		time.Sleep(time.Second)
	}
	if alloc == nil {
		return "", 0, fmt.Errorf("nomad: job %q has no placement after 30s (node pool or GPU device unavailable?)", jobID)
	}
	// The list stub never carries AllocatedResources on this endpoint (Nomad only
	// honors resources=true on /v1/allocations), so read ports from the full alloc.
	full, _, err := n.c.Allocations().Info(alloc.ID, qo)
	if err != nil {
		return "", 0, fmt.Errorf("nomad: allocation info %q: %w", alloc.ID, err)
	}
	if full.AllocatedResources != nil {
		for _, p := range full.AllocatedResources.Shared.Ports {
			if p.Label == "http" {
				port = p.Value
				break
			}
		}
	}
	node, _, err := n.c.Nodes().Info(alloc.NodeID, qo)
	if err != nil {
		return "", 0, fmt.Errorf("nomad: node info %q: %w", alloc.NodeID, err)
	}
	if v := node.Attributes["unique.platform.aws.local-ipv4"]; v != "" {
		return v, port, nil
	}
	if host, _, err := net.SplitHostPort(node.HTTPAddr); err == nil && host != "" {
		return host, port, nil
	}
	return "", 0, fmt.Errorf("nomad: could not resolve private IP for node %q", alloc.NodeID)
}

// RegisterJob parses the rendered HCL on the server (so HCL2 functions resolve
// exactly as the CLI would) and registers the resulting job in namespace. The
// flavor (template name) is stamped into the job's Meta so the listing can show
// which template produced a workspace (the rendered HCL doesn't carry it).
func (n *Nomad) RegisterJob(namespace, jobHCL, flavor string) (string, error) {
	job, err := n.c.Jobs().ParseHCL(jobHCL, true)
	if err != nil {
		return "", fmt.Errorf("nomad: parse job hcl: %w", err)
	}
	job.Namespace = &namespace
	if flavor != "" {
		if job.Meta == nil {
			job.Meta = map[string]string{}
		}
		job.Meta["flavor"] = flavor
	}
	if _, _, err := n.c.Jobs().Register(job, &napi.WriteOptions{Namespace: namespace}); err != nil {
		return "", fmt.Errorf("nomad: register job: %w", err)
	}
	if job.ID != nil {
		return *job.ID, nil
	}
	return "", nil
}

// StopJob stops the job without purging it, so Nomad retains the definition and
// StartJob can bring it back. The home volume and Boundary access are untouched.
func (n *Nomad) StopJob(namespace, jobID string) error {
	if _, _, err := n.c.Jobs().Deregister(jobID, false, &napi.WriteOptions{Namespace: namespace}); err != nil {
		return fmt.Errorf("nomad: stop job %q: %w", jobID, err)
	}
	return nil
}

// StartJob restarts a previously stopped job by clearing its Stop flag and
// re-registering the definition Nomad retained.
func (n *Nomad) StartJob(namespace, jobID string) error {
	job, _, err := n.c.Jobs().Info(jobID, &napi.QueryOptions{Namespace: namespace})
	if err != nil {
		return fmt.Errorf("nomad: job info %q: %w", jobID, err)
	}
	stop := false
	job.Stop = &stop
	if _, _, err := n.c.Jobs().Register(job, &napi.WriteOptions{Namespace: namespace}); err != nil {
		return fmt.Errorf("nomad: start job %q: %w", jobID, err)
	}
	return nil
}

// PurgeJob stops and fully removes the job (deregister with purge).
func (n *Nomad) PurgeJob(namespace, jobID string) error {
	if _, _, err := n.c.Jobs().Deregister(jobID, true, &napi.WriteOptions{Namespace: namespace}); err != nil {
		return fmt.Errorf("nomad: purge job %q: %w", jobID, err)
	}
	return nil
}

// DeleteHostVolume deletes the named per-workspace CSI volume in namespace,
// mirroring `nomad volume delete <id>`: Nomad resolves the provider id, deletes
// the backing EBS volume through the controller, and drops its own registration.
// Idempotent: a volume already gone is a no-op. Called from workspace Destroy
// (single volume) and project teardown (sweep); workspace Stop leaves it intact.
func (n *Nomad) DeleteHostVolume(namespace, name string) error {
	vol, _, err := n.c.CSIVolumes().Info(name, &napi.QueryOptions{Namespace: namespace})
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return fmt.Errorf("nomad: get CSI volume %q: %w", name, err)
	}
	// The delete endpoint takes the Nomad volume id (the request field is named
	// ExternalVolumeID for historical reasons, but `nomad volume delete <id>`
	// passes the Nomad id here and Nomad resolves the backing EBS volume).
	if err := n.c.CSIVolumes().DeleteOpts(&napi.CSIVolumeDeleteRequest{ExternalVolumeID: vol.ID}, &napi.WriteOptions{Namespace: namespace}); err != nil {
		if isNotFound(err) {
			return nil
		}
		return fmt.Errorf("nomad: delete CSI volume %q: %w", name, err)
	}
	return nil
}

// isNotFound reports whether a Nomad API error is a 404 / "not found", used to
// keep volume deletes idempotent.
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "not found") || strings.Contains(s, "404")
}

// JobExists reports whether a job of the given id already runs in namespace.
func (n *Nomad) JobExists(namespace, jobID string) (bool, error) {
	_, _, err := n.c.Jobs().Info(jobID, &napi.QueryOptions{Namespace: namespace})
	if err != nil {
		if strings.Contains(err.Error(), "404") || strings.Contains(strings.ToLower(err.Error()), "not found") {
			return false, nil
		}
		return false, fmt.Errorf("nomad: job info %q: %w", jobID, err)
	}
	return true, nil
}

// WorkspaceJob is a thin view of a developer's workspace job for the portal UI.
type WorkspaceJob struct {
	ID     string
	Name   string
	Status string
	Port   int
	Flavor string // template name stamped into the job Meta at register time
}

// ListWorkspaceJobs returns jobs in namespace whose name starts with prefix,
// with the workspace's static SSH port and template (flavor) resolved from the
// full job (one Info read per workspace job).
func (n *Nomad) ListWorkspaceJobs(namespace, prefix string) ([]WorkspaceJob, error) {
	stubs, _, err := n.c.Jobs().List(&napi.QueryOptions{Namespace: namespace})
	if err != nil {
		return nil, fmt.Errorf("nomad: list jobs: %w", err)
	}
	var out []WorkspaceJob
	for _, s := range stubs {
		if !strings.HasPrefix(s.Name, prefix) {
			continue
		}
		port := 0
		flavor := ""
		if job, _, err := n.c.Jobs().Info(s.ID, &napi.QueryOptions{Namespace: namespace}); err == nil {
			if ports := portsFromJob(job); len(ports) > 0 {
				port = ports[0]
			}
			flavor = job.Meta["flavor"]
		}
		out = append(out, WorkspaceJob{ID: s.ID, Name: s.Name, Status: n.workspaceStatus(namespace, s.ID, s.Status), Port: port, Flavor: flavor})
	}
	return out, nil
}

// workspaceStatus refines the job-level status into one that reflects whether the
// workspace is actually reachable. A freshly-registered service job reports
// "running" the moment it is scheduled, while its allocation is still pulling the
// image and rendering secrets and sshd is not yet up — so the card would invite
// the developer to connect (and fail) too early. Even once the container process
// starts (ClientStatus "running") Nomad keeps the deployment "in progress" until
// the allocation is marked healthy; we mirror that, reporting "pending" until the
// latest allocation's deployment health is true. A failed placement is surfaced
// as-is and a stopped job ("dead") is reported verbatim.
func (n *Nomad) workspaceStatus(namespace, jobID, jobStatus string) string {
	if jobStatus == "dead" {
		return "dead"
	}
	qo := &napi.QueryOptions{Namespace: namespace}
	allocs, _, err := n.c.Jobs().Allocations(jobID, false, qo)
	if err != nil || len(allocs) == 0 {
		return "pending"
	}
	latest := allocs[0]
	for _, a := range allocs[1:] {
		if a.ModifyIndex > latest.ModifyIndex {
			latest = a
		}
	}
	switch latest.ClientStatus {
	case "running":
		// The container is up, but the deployment is still "in progress" until
		// the allocation passes its health gate. Only then is the workspace
		// actually ready to accept connections.
		if latest.DeploymentStatus != nil && latest.DeploymentStatus.Healthy != nil && *latest.DeploymentStatus.Healthy {
			return "running"
		}
		return "pending"
	case "failed", "lost":
		return latest.ClientStatus
	default: // pending, or not yet set
		return "pending"
	}
}

// JobLogs returns the tail (up to maxBytes) of the most recent allocation's
// stdout or stderr for the job. Nomad proxies the alloc-fs read through the
// server address the portal already uses, so no direct client connection is
// needed on the single-node stack.
func (n *Nomad) JobLogs(namespace, jobID, logType string, maxBytes int64) (string, error) {
	if logType != "stdout" && logType != "stderr" {
		return "", fmt.Errorf("nomad: invalid log type %q (want stdout or stderr)", logType)
	}
	qo := &napi.QueryOptions{Namespace: namespace}
	stubs, _, err := n.c.Jobs().Allocations(jobID, false, qo)
	if err != nil {
		return "", fmt.Errorf("nomad: list allocations for %q: %w", jobID, err)
	}
	if len(stubs) == 0 {
		return "", fmt.Errorf("nomad: job %q has no allocations yet", jobID)
	}
	latest := stubs[0]
	for _, s := range stubs[1:] {
		if s.ModifyIndex > latest.ModifyIndex {
			latest = s
		}
	}
	alloc, _, err := n.c.Allocations().Info(latest.ID, qo)
	if err != nil {
		return "", fmt.Errorf("nomad: alloc info %q: %w", latest.ID, err)
	}

	cancel := make(chan struct{})
	defer close(cancel)
	frames, errCh := n.c.AllocFS().Logs(alloc, false, taskName(alloc), logType, "end", maxBytes, cancel, qo)
	var b strings.Builder
	for {
		select {
		case f, ok := <-frames:
			if !ok {
				return b.String(), nil
			}
			if f != nil && len(f.Data) > 0 {
				b.Write(f.Data)
			}
		case e, ok := <-errCh:
			if !ok {
				errCh = nil // disable this case; wait for frames to close
				continue
			}
			if e != nil {
				return b.String(), fmt.Errorf("nomad: stream %s logs: %w", logType, e)
			}
		}
	}
}

// taskName picks the task to read logs from: the workspace task by name, else
// the allocation's only task. The dev-workspace job has a single task.
func taskName(alloc *napi.Allocation) string {
	if _, ok := alloc.TaskStates["workspace"]; ok {
		return "workspace"
	}
	for name := range alloc.TaskStates {
		return name
	}
	return "workspace"
}

// UsedPorts returns every static host port reserved by any job in any namespace
// (workspace SSH ports are node-global, so collisions must be avoided globally).
func (n *Nomad) UsedPorts() ([]int, error) {
	stubs, _, err := n.c.Jobs().List(&napi.QueryOptions{Namespace: "*"})
	if err != nil {
		return nil, fmt.Errorf("nomad: list jobs (all namespaces): %w", err)
	}
	var ports []int
	for _, s := range stubs {
		p, err := n.jobPorts(s.Namespace, s.ID)
		if err != nil {
			continue // a transient read error must not block allocation
		}
		ports = append(ports, p...)
	}
	return ports, nil
}

func (n *Nomad) jobPorts(namespace, jobID string) ([]int, error) {
	job, _, err := n.c.Jobs().Info(jobID, &napi.QueryOptions{Namespace: namespace})
	if err != nil {
		return nil, err
	}
	return portsFromJob(job), nil
}

// portsFromJob extracts the static reserved host ports declared in a job's
// network blocks.
func portsFromJob(job *napi.Job) []int {
	var ports []int
	for _, tg := range job.TaskGroups {
		for _, net := range tg.Networks {
			for _, p := range net.ReservedPorts {
				if p.Value > 0 {
					ports = append(ports, p.Value)
				}
			}
		}
	}
	return ports
}
