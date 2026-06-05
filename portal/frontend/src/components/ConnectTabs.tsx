import { useState } from 'react'
import {
  Button,
  CodeSnippet,
  Dropdown,
  InlineNotification,
  ListItem,
  OrderedList,
  Tab,
  TabList,
  TabPanel,
  TabPanels,
  Tabs,
} from '@carbon/react'
import { Launch } from '@carbon/icons-react'
import { Workspace, writeSSHConfig } from '../api/client'
import { useMe } from '../me'

// IDEs in the VS Code family share the `vscode-remote/ssh-remote+<host>` deep
// link authority and differ only by URL scheme, so one list covers them all.
const IDES = [
  { id: 'vscode', label: 'VS Code', scheme: 'vscode' },
  { id: 'vscode-insiders', label: 'VS Code Insiders', scheme: 'vscode-insiders' },
  { id: 'cursor', label: 'Cursor', scheme: 'cursor' },
  { id: 'windsurf', label: 'Windsurf', scheme: 'windsurf' },
]

// Two connection methods on the workspace card: the ProxyCommand path (no Client
// Agent, better speed) and the transparent-session path. Both need a one-time
// Boundary SSO login.
export default function ConnectTabs({ ws }: { ws: Workspace }) {
  const me = useMe()
  const [ide, setIde] = useState(IDES[0])
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')

  const openInIDE = async () => {
    setBusy(true)
    setErr('')
    try {
      const host = await writeSSHConfig(ws.project, ws.name)
      window.location.assign(`${ide.scheme}://vscode-remote/ssh-remote+${host}/home/dev/project`)
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Tabs>
      <TabList aria-label="Connection method" contained>
        <Tab>VSCode (ProxyCommand)</Tab>
        <Tab>Transparent</Tab>
      </TabList>
      <TabPanels>
        <TabPanel>
          <OrderedList style={{ marginBottom: '0.5rem' }}>
            <ListItem>One time per terminal session, authenticate to Boundary:</ListItem>
          </OrderedList>
          <CodeSnippet type="single" feedback="Copied!">
            {ws.boundary_authenticate_cmd}
          </CodeSnippet>

          {me?.local_ssh ? (
            <>
              <OrderedList style={{ margin: '0.75rem 0 0.5rem' }}>
                <ListItem>
                  Then open it directly — the portal writes the SSH config to <code>~/.ssh/config</code> and launches
                  your IDE:
                </ListItem>
              </OrderedList>
              <div style={{ display: 'flex', alignItems: 'flex-end', gap: '0.5rem' }}>
                <Dropdown
                  id={`ide-${ws.name}`}
                  size="sm"
                  label="IDE"
                  titleText="IDE"
                  items={IDES}
                  itemToString={(i) => (i ? i.label : '')}
                  selectedItem={ide}
                  onChange={({ selectedItem }) => selectedItem && setIde(selectedItem)}
                  style={{ minWidth: '12rem' }}
                />
                <Button size="sm" renderIcon={Launch} disabled={busy} onClick={openInIDE}>
                  Open in {ide.label}
                </Button>
              </div>
              {err && (
                <InlineNotification
                  kind="error"
                  title="Could not open"
                  subtitle={err}
                  lowContrast
                  onClose={() => setErr('')}
                />
              )}
              <details style={{ marginTop: '0.75rem' }}>
                <summary style={{ cursor: 'pointer', fontSize: '0.8rem' }}>Or configure manually</summary>
                <CodeSnippet type="multi" feedback="Copied!">
                  {ws.proxycommand_config}
                </CodeSnippet>
              </details>
            </>
          ) : (
            <>
              <OrderedList style={{ margin: '0.75rem 0 0.5rem' }}>
                <ListItem>
                  Append this to <code>~/.ssh/config</code>, then in VSCode run “Remote-SSH: Connect to Host…” and pick{' '}
                  <code>{ws.name}</code>:
                </ListItem>
              </OrderedList>
              <CodeSnippet type="multi" feedback="Copied!">
                {ws.proxycommand_config}
              </CodeSnippet>
            </>
          )}
        </TabPanel>
        <TabPanel>
          <OrderedList style={{ margin: '0 0 0.5rem' }}>
            <ListItem>
              Requires the Boundary Client Agent running plus an SSO login. Append to <code>~/.ssh/config</code> and
              connect to <code>{ws.alias}</code>:
            </ListItem>
          </OrderedList>
          <CodeSnippet type="multi" feedback="Copied!">
            {ws.transparent_config}
          </CodeSnippet>
        </TabPanel>
      </TabPanels>
    </Tabs>
  )
}
