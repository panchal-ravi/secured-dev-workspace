import { useState } from 'react'
import {
  Button,
  CodeSnippet,
  Dropdown,
  ListItem,
  OrderedList,
  Tab,
  TabList,
  TabPanel,
  TabPanels,
  Tabs,
} from '@carbon/react'
import { Launch } from '@carbon/icons-react'
import { Workspace, connectLink } from '../api/client'

// IDEs in the VS Code family share the `vscode-remote/ssh-remote+<host>` deep
// link authority and differ only by URL scheme; the helper maps these ids to the
// right scheme when it opens the IDE.
const IDES = [
  { id: 'vscode', label: 'VS Code' },
  { id: 'vscode-insiders', label: 'VS Code Insiders' },
  { id: 'cursor', label: 'Cursor' },
  { id: 'windsurf', label: 'Windsurf' },
]

// Two connection methods on the workspace card: the ProxyCommand path (no Client
// Agent, better speed) and the transparent-session path. With a remote backend,
// the ProxyCommand "Open" hands a secured-ws://connect link to the local helper,
// which signs in to Boundary on demand (reusing the portal's Verify SSO session,
// no re-login), writes ~/.ssh/config, and launches the IDE — one click. The
// transparent path still needs the Boundary Client Agent + a manual SSO login.
export default function ConnectTabs({ ws }: { ws: Workspace }) {
  const [ide, setIde] = useState(IDES[0])
  const running = ws.status === 'running'

  const open = () => {
    window.location.assign(
      connectLink({
        host: ws.name,
        user: ws.user,
        targetId: ws.target_id,
        addr: ws.boundary_addr,
        ide: ide.id,
        authMethodId: ws.boundary_auth_method_id,
      }),
    )
  }

  return (
    <Tabs>
      <TabList aria-label="Connection method" contained>
        <Tab>VSCode (ProxyCommand)</Tab>
        <Tab>Transparent</Tab>
      </TabList>
      <TabPanels>
        <TabPanel>
          <OrderedList style={{ margin: '0.5rem 0' }}>
            <ListItem>
              Open it directly — the Secured Workspace helper signs you in to Boundary if needed (reusing your portal
              login), writes the SSH config to <code>~/.ssh/config</code>, and launches your IDE:
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
            <Button size="sm" renderIcon={Launch} disabled={!running} onClick={open}>
              Open
            </Button>
          </div>
          {!running && (
            <p style={{ color: 'var(--cds-text-secondary)', fontSize: '0.8rem', marginTop: '0.5rem' }}>
              Available once the workspace is running.
            </p>
          )}
          <details style={{ marginTop: '0.75rem' }}>
            <summary style={{ cursor: 'pointer', fontSize: '0.8rem' }}>Or configure manually</summary>
            <CodeSnippet type="multi" feedback="Copied!">
              {ws.proxycommand_config}
            </CodeSnippet>
          </details>
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
