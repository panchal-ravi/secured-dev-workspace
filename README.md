# Secured Remote Dev Workspace

**Secure, self-hosted, network-isolated, agent-ready dev workspaces.** Instead of cloning
source code, installing tools, and storing credentials on personal laptops, each developer
codes inside a **central, secured workspace** reached through their normal IDE — with an AI
coding agent already running inside it. The source code, the tools, the secrets, and the AI
all stay protected on your own infrastructure — never on the laptop, never on the open
internet.

*For the technical architecture and how the system is built and operated, see
[`ARCHITECTURE.md`](ARCHITECTURE.md).*

## The solution in 90 seconds

<!-- The video is a user-attachment of demo/animated-short/short/out/short.mp4.
     Re-render with: cd demo/animated-short/short && node record.mjs — then drag the
     new mp4 into the GitHub web editor and put the generated URL into the video tag's
     src. GitHub always shows the uploaded file's name in a header above the player
     (not removable), so give the file a presentable name before uploading. -->

<video src="https://github.ibm.com/user-attachments/assets/966aff49-3582-4dbe-bbde-00cc033a44f9" controls></video>

## Why it's more secure

**Your source code never lives on a laptop.**
The repository, the build tools, and the AI coding assistants all run in a protected
workspace in your environment. If a laptop is lost or stolen, there's no code and no
password sitting on it to leak.

**Developers don't hold any keys or passwords for the workspace.**
There are no SSH keys to manage, lose, or accidentally share. Each time a developer
connects, the system issues a fresh, short-lived access credential that expires
automatically. Nothing long-lived is ever handed out.

**Pushing code needs no stored token either.**
The workspace arrives ready for git — already set up as the signed-in developer — so
there's nothing to configure. When code is pushed back to the company's source host, the
credential is issued fresh on demand, is short-lived, and is never written to disk.
There is no personal access token or password to store, leak, or rotate, and every commit
still carries the real author's name.

**Every connection is verified and access is granted person-by-person.**
Developers sign in with your company's existing single sign-on (IBM Verify, or any
standard identity provider). Each
developer can reach **only their own** workspace — never anyone else's. This is enforced
by the system, not just by policy, and we test it: a second developer is actively
blocked from another developer's workspace.

**The workspace can't be reached from the open internet.**
There is no public door into the workspace. The only way in is through a secured access
broker that checks who you are first.

**Each project is walled off from every other.**
Every project runs in its own isolated space. One project — and one developer — cannot
see or touch another's workspace, data, or credentials.

**The riskiest work can run behind a hardware wall.**
For the strongest isolation, a workspace can run inside a microVM — a lightweight
virtual machine with its own operating-system kernel, the same kind of boundary cloud
providers use to keep different customers apart. Everything inside it — including any
code the AI agent writes and runs — is contained by hardware virtualization, not just
container walls, so even a misbehaving workload cannot reach the host machine or other
workspaces.

**The AI assistant's reach is controlled and contained.**
The AI coding assistant runs inside the workspace — never on the laptop — and reaches its
tools and data through a central AI gateway. Each workspace can use only the tools and
data approved for its project, and that access is controlled and audited in one place
rather than wired into each workspace separately.

**Everything is auditable.**
Every access is tied to a named person, so there's a clear record of who connected to
what, and when.

## The technology behind it

The platform is built on proven **IBM** and **HashiCorp** technology, each part with a
distinct security role:

- **Boundary (HashiCorp) — the secured front door.** Every connection to a workspace goes
  through Boundary. It verifies who the developer is, checks they're allowed to reach that
  specific workspace, and brokers the session — so the workspace itself is never exposed
  to the network. Developers connect by identity, not by knowing a server address or
  holding a key.

- **Vault (HashiCorp) — the credential authority.** Vault issues the short-lived access
  credentials on demand — both for connecting to a workspace and for pushing code to the
  source host — each one expiring automatically. Nothing long-lived is ever stored or
  handed out. Vault also keeps each project's credentials separate, so they can never
  cross between projects.

- **Nomad (HashiCorp) — the workspace host.** Nomad runs the developer workspaces as
  isolated, managed containers. It keeps each workspace running and saved between sessions,
  and places every project in its own separated space so workloads can't interfere with
  one another. The same approach runs equally on Kubernetes where that's the standard.

- **AI / MCP gateway (IBM) — the AI control point.** A central gateway governs how the
  in-workspace AI assistant reaches tools and data. Each workspace can use only the tools
  and data approved for its project, controlled and audited in one place rather than wired
  into each workspace separately.

- **IBM Verify — single sign-on.** Identity comes from your company's existing single
  sign-on. IBM Verify is used here, but any standard identity provider works in its place.

Together: **Nomad** runs the workspace, **Vault** issues the short-lived credentials,
**Boundary** verifies the developer and connects them, and the **AI gateway** governs what
the workspace's AI assistant can reach — all tied to your company's single sign-on.

## How a developer uses it

1. Sign in with your normal company login (single sign-on).
2. Create your workspace from the self-service portal — pick the ready-made template
   that fits the job: standard, GPU-backed for heavier workloads, or microVM for
   hardware-grade isolation.
3. Open your workspace directly from your IDE (for example, VS Code).
4. Start coding. Your project is already there, the tools are installed, git is set up
   as you, and the AI coding assistant is ready — connected to your project's
   approved tools and data. Your work is saved between sessions; commit and push as you
   normally would.

No keys to copy. No tools to install. No source code to download. No tokens to paste.

## Self-service developer portal

Workspaces can be set up by the platform team — and, increasingly, by developers
themselves. A **self-service developer portal** (currently a working preview) lets
developers request, start, stop, and manage their own secured workspaces from a simple
web interface, choosing from ready-made workspace types — including **GPU-backed** ones for
heavier workloads and **microVM-isolated** ones that run the AI agent's code behind a
hardware-virtualization boundary — with no need to understand or run any of the underlying
infrastructure tooling. The same security guarantees above apply automatically, whichever
path is used.

What's next: maturing the portal, durable storage so a workspace survives a full machine
replacement, and making the hardware-isolated workspace the default.
