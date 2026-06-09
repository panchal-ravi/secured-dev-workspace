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

// CreateHostVolume creates the persistent /home/dev dynamic host volume (mkdir
// plugin, single-node-writer/file-system), mirroring the dev-workspace tier. The
// volume is pinned to a specific ready node in the job's target node pool so it
// materializes on a node the job can actually be placed on: a non-empty nodePool
// (e.g. "gpu") selects a node there, and an empty nodePool means the implicit
// "default" pool (the main node). Pinning to a resolved ready node (rather than
// letting the server place by pool alone) is what keeps the default case off the
// GPU node, and also skips any stale "down" node a GPU destroy/reprovision cycle
// leaves in the pool — Nomad's pool placement does not filter those out and would
// fail with "No path to node".
func (n *Nomad) CreateHostVolume(namespace, name, nodePool string) error {
	pool := nodePool
	if pool == "" {
		pool = "default"
	}
	nodeID, err := n.readyNodeInPool(pool)
	if err != nil {
		return err
	}
	vol := &napi.HostVolume{
		Namespace: namespace,
		Name:      name,
		PluginID:  "mkdir",
		NodePool:  pool,
		NodeID:    nodeID,
		RequestedCapabilities: []*napi.HostVolumeCapability{{
			AccessMode:     napi.HostVolumeAccessModeSingleNodeWriter,
			AttachmentMode: napi.HostVolumeAttachmentModeFilesystem,
		}},
	}
	resp, _, err := n.c.HostVolumes().Create(&napi.HostVolumeCreateRequest{Volume: vol}, &napi.WriteOptions{Namespace: namespace})
	if err != nil {
		return fmt.Errorf("nomad: create host volume %q: %w", name, err)
	}
	// Create returns as soon as the request is accepted; the mkdir plugin then
	// materializes the volume on the node asynchronously (pending -> ready). The
	// scheduler excludes the node while the volume is pending, so a job registered
	// in that window fails placement with "missing compatible host volumes" and
	// lands in a blocked eval that a later volume-ready transition does not
	// reliably re-trigger. Wait for ready here so the caller registers the job
	// only once placement can actually succeed.
	if err := n.waitHostVolumeReady(resp.Volume.ID, name, namespace); err != nil {
		return err
	}
	return nil
}

// waitHostVolumeReady polls a dynamic host volume until it reports ready, so the
// workspace job is registered only after the volume can satisfy placement.
// Bounded: a stuck/misconfigured plugin surfaces a clear error instead of hanging.
func (n *Nomad) waitHostVolumeReady(id, name, namespace string) error {
	qo := &napi.QueryOptions{Namespace: namespace}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		vol, _, err := n.c.HostVolumes().Get(id, qo)
		if err != nil {
			return fmt.Errorf("nomad: get host volume %q: %w", name, err)
		}
		switch vol.State {
		case napi.HostVolumeStateReady:
			return nil
		case napi.HostVolumeStateUnavailable:
			return fmt.Errorf("nomad: host volume %q is unavailable", name)
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("nomad: host volume %q not ready after 30s", name)
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
	qo := &napi.QueryOptions{Namespace: namespace}
	var nodeID string
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		allocs, _, err := n.c.Jobs().Allocations(jobID, false, qo)
		if err != nil {
			return "", fmt.Errorf("nomad: list allocations for %q: %w", jobID, err)
		}
		if len(allocs) > 0 && allocs[0].NodeID != "" {
			nodeID = allocs[0].NodeID
			break
		}
		time.Sleep(time.Second)
	}
	if nodeID == "" {
		return "", fmt.Errorf("nomad: job %q has no placement after 30s (node pool or GPU device unavailable?)", jobID)
	}
	node, _, err := n.c.Nodes().Info(nodeID, qo)
	if err != nil {
		return "", fmt.Errorf("nomad: node info %q: %w", nodeID, err)
	}
	if ip := node.Attributes["unique.platform.aws.local-ipv4"]; ip != "" {
		return ip, nil
	}
	if host, _, err := net.SplitHostPort(node.HTTPAddr); err == nil && host != "" {
		return host, nil
	}
	return "", fmt.Errorf("nomad: could not resolve private IP for node %q", nodeID)
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

// DeleteHostVolume removes the named dynamic host volume in namespace. The
// volume API deletes by id, so the name is resolved via a list. Idempotent: a
// volume already gone is a no-op. Force handles a volume still releasing from a
// just-purged allocation.
func (n *Nomad) DeleteHostVolume(namespace, name string) error {
	vols, _, err := n.c.HostVolumes().List(&napi.HostVolumeListRequest{}, &napi.QueryOptions{Namespace: namespace})
	if err != nil {
		return fmt.Errorf("nomad: list host volumes: %w", err)
	}
	for _, v := range vols {
		if v.Name == name {
			if _, _, err := n.c.HostVolumes().Delete(&napi.HostVolumeDeleteRequest{ID: v.ID, Force: true}, &napi.WriteOptions{Namespace: namespace}); err != nil {
				return fmt.Errorf("nomad: delete host volume %q: %w", name, err)
			}
			return nil
		}
	}
	return nil
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
