# Secured Remote Dev Workspace

A safer way for developers to write code. Instead of cloning source code, installing
tools, and storing credentials on personal laptops, each developer works inside a
**central, secured workspace** that they reach through their normal IDE. The code, the
tools, and the secrets stay protected on the company's infrastructure — never on the
laptop.

## Why it's more secure

**Your source code never lives on a laptop.**
The repository, the build tools, and the AI coding assistants all run in a protected
workspace in your environment. If a laptop is lost or stolen, there's no code and no
password sitting on it to leak.

**Developers don't hold any keys or passwords for the workspace.**
There are no SSH keys to manage, lose, or accidentally share. Each time a developer
connects, the system issues a fresh access credential that automatically expires after
a few minutes. Nothing long-lived is ever handed out.

**Pushing code needs no stored token either.**
The workspace arrives ready for git — already set up as the signed-in developer — so
there's nothing to configure. When code is pushed back to the company's source host, the
credential is issued fresh on demand, lasts about an hour, and is never written to disk.
There is no personal access token or password to store, leak, or rotate, and every commit
still carries the real author's name.

**Every connection is verified and access is granted person-by-person.**
Developers sign in with your company's existing single sign-on (IBM Verify). Each
developer can reach **only their own** workspace — never anyone else's. This is enforced
by the system, not just by policy, and we test it: a second developer is actively
blocked from another developer's workspace.

**The workspace can't be reached from the open internet.**
There is no public door into the workspace. The only way in is through a secured access
broker that checks who you are first. Network access is also restricted to known,
approved locations.

**Each project is walled off from every other.**
Every project runs in its own isolated space. One project — and one developer — cannot
see or touch another's workspace, data, or credentials.

**Everything is auditable.**
Every access is tied to a named person, so there's a clear record of who connected to
what, and when.

## The technology behind it

The platform is built on three proven HashiCorp products, each with a distinct security
role:

- **Boundary — the secured front door.** Every connection to a workspace goes through
  Boundary. It verifies who the developer is, checks they're allowed to reach that
  specific workspace, and brokers the session — so the workspace itself is never exposed
  to the network. Developers connect by identity, not by knowing a server address or
  holding a key.

- **Vault — the credential authority.** Vault issues the short-lived access credentials
  on demand — both for connecting to a workspace and for pushing code to the source host —
  each one expiring within minutes to an hour. Nothing long-lived is ever stored or handed
  out. Vault also keeps each project's credentials separate, so they can never cross
  between projects.

- **Nomad — the workspace host.** Nomad runs the developer workspaces as isolated,
  managed containers. It keeps each workspace running and saved between sessions, and
  places every project in its own separated space so workloads can't interfere with one
  another.

Together: **Nomad** runs the workspace, **Vault** issues the temporary credential, and
**Boundary** verifies the developer and connects them — with identity provided by your
company's single sign-on.

## How a developer uses it

1. Sign in with your normal company login (single sign-on).
2. Open your workspace directly from your IDE (for example, VS Code).
3. Start coding. Your project is already there, the tools are installed, git is set up
   as you, and your work is saved between sessions. Commit and push as you normally would.

No keys to copy. No tools to install. No source code to download. No tokens to paste.

## What's next

Today, workspaces are set up by the platform team. We're evolving this into a
**self-service developer portal**: developers will request, start, stop, and manage
their own secured workspaces through a simple web interface — with no need to understand
or run any of the underlying infrastructure tooling. The same security guarantees above
will continue to apply, automatically.

---

*For the technical architecture and how the system is built and operated, see
[`ARCHITECTURE.md`](ARCHITECTURE.md).*
