import { useState } from 'react'
import { Accordion, AccordionItem, Button, CodeSnippet, Dropdown } from '@carbon/react'
import { Terminal } from '@carbon/icons-react'
import { boundaryAuthLink, HELPER_DOWNLOAD, TerminalOption } from '../api/client'

// browserTerminals lists the terminals to offer based on the OS of the machine
// running the browser — that is the developer's own machine, where the helper
// opens the terminal. Empty on any OS we can't drive, which hides the launch
// button. Warp is intentionally excluded (no reliable scripting hook).
function browserTerminals(): TerminalOption[] {
  const uaData = (navigator as unknown as { userAgentData?: { platform?: string } }).userAgentData
  const platform = (uaData?.platform || navigator.platform || navigator.userAgent || '').toLowerCase()
  if (platform.includes('mac')) {
    return [
      { id: 'terminal', label: 'Terminal (built-in)' },
      { id: 'iterm', label: 'iTerm2' },
    ]
  }
  if (platform.includes('win')) {
    return [
      { id: 'wt', label: 'Windows Terminal' },
      { id: 'powershell', label: 'PowerShell' },
      { id: 'cmd', label: 'Command Prompt' },
    ]
  }
  return []
}

const TERMINALS = browserTerminals()

// currentOS detects the developer's own machine (the one running the browser),
// so the install steps below match where the helper actually gets installed.
function currentOS(): 'mac' | 'win' | 'linux' | 'other' {
  const uaData = (navigator as unknown as { userAgentData?: { platform?: string } }).userAgentData
  const platform = (uaData?.platform || navigator.platform || navigator.userAgent || '').toLowerCase()
  if (platform.includes('mac')) return 'mac'
  if (platform.includes('win')) return 'win'
  if (platform.includes('linux') || platform.includes('x11')) return 'linux'
  return 'other'
}

const OS = currentOS()

// The browser downloads the zip to ~/Downloads; unzip to ~/Applications, strip the
// quarantine flag (the app is ad-hoc signed — without this, macOS Gatekeeper reports
// it as "damaged" / "could not verify"), then open it once to register secured-ws://.
const MAC_INSTALL = `unzip ~/Downloads/SecuredWS-macos.zip -d ~/Applications
xattr -dr com.apple.quarantine ~/Applications/SecuredWS.app
open ~/Applications/SecuredWS.app`

// Project-level Boundary login panel, shown once above all of a project's
// workspace cards. Both connection methods need a one-time-per-terminal-session
// Boundary token, so the instruction lives here rather than on every card. The
// backend is remote, so the Authenticate button hands a secured-ws://authenticate
// link to the locally-installed helper, which opens the chosen terminal running
// the login.
export default function BoundaryAuth({
  project,
  command,
  addr,
  authMethodId,
}: {
  project: string
  command: string
  addr: string
  authMethodId: string
}) {
  const [term, setTerm] = useState<TerminalOption | undefined>(undefined)
  const selected = term ?? TERMINALS[0]

  const authenticate = () => {
    window.location.assign(boundaryAuthLink(addr, authMethodId, selected?.id ?? ''))
  }

  return (
    <div style={{ marginBottom: '1rem' }}>
      <Accordion>
        <AccordionItem title="Connect to your workspaces">
          <p style={{ color: 'var(--cds-text-secondary)', fontSize: '0.8rem', margin: '0 0 0.75rem' }}>
            Authenticate to Boundary once per terminal session before opening any workspace below.
          </p>
          <div style={{ display: 'flex', gap: '0.75rem', alignItems: 'flex-start', flexWrap: 'wrap' }}>
            <div style={{ flex: '1 1 24rem', minWidth: '18rem' }}>
              <CodeSnippet type="single" feedback="Copied!">
                {command}
              </CodeSnippet>
            </div>
            {TERMINALS.length > 0 && (
              <div style={{ display: 'flex', alignItems: 'flex-start', gap: '0.5rem' }}>
                <Dropdown
                  id={`term-${project}`}
                  size="md"
                  label="Terminal"
                  titleText="Terminal"
                  hideLabel
                  items={TERMINALS}
                  itemToString={(t) => (t ? t.label : '')}
                  selectedItem={selected ?? null}
                  onChange={({ selectedItem }) => selectedItem && setTerm(selectedItem)}
                  style={{ minWidth: '12rem' }}
                />
                <Button size="md" renderIcon={Terminal} onClick={authenticate}>
                  Authenticate
                </Button>
              </div>
            )}
          </div>
          <div style={{ marginTop: '0.75rem' }}>
            <Accordion size="sm">
              <AccordionItem title="First time? Set up the Secured Workspace helper">
                <div style={{ fontSize: '0.75rem' }}>
                  {OS === 'mac' && (
                    <>
                      <p style={{ color: 'var(--cds-text-secondary)', margin: '0 0 0.5rem' }}>
                        <a href={HELPER_DOWNLOAD}>Download the helper</a>, then run these in Terminal (don&apos;t
                        re-download after — a fresh download re-applies the quarantine flag):
                      </p>
                      <CodeSnippet type="multi" feedback="Copied!">
                        {MAC_INSTALL}
                      </CodeSnippet>
                      <p style={{ color: 'var(--cds-text-secondary)', margin: '0.5rem 0 0' }}>
                        The <code>xattr</code> step is required: the helper is ad-hoc signed, so without it macOS
                        Gatekeeper blocks it as &quot;damaged&quot; / &quot;could not verify&quot;.
                      </p>
                    </>
                  )}
                  {OS !== 'mac' && (
                    <p style={{ color: 'var(--cds-text-secondary)', margin: 0 }}>
                      The helper is currently packaged for macOS only. {OS === 'win' ? 'Windows' : OS === 'linux' ? 'Linux' : 'Your OS'}{' '}
                      install steps (registry / <code>.desktop</code> handler) are documented in{' '}
                      <code>portal/helper/README.md</code>.
                    </p>
                  )}
                </div>
              </AccordionItem>
            </Accordion>
          </div>
        </AccordionItem>
      </Accordion>
    </div>
  )
}
