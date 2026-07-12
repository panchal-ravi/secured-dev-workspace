import { useEffect, useState } from 'react'
import { useParams } from 'react-router-dom'
import {
  Button,
  Checkbox,
  InlineNotification,
  Loading,
  Select,
  SelectItem,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableHeader,
  TableRow,
  Tag,
  TextInput,
} from '@carbon/react'
import { Add, TrashCan } from '@carbon/icons-react'
import {
  listProjectRoles,
  grantProjectRole,
  revokeProjectRole,
  getProjectCapabilities,
  putProjectCapabilities,
  ProjectRole,
} from '../../api/client'

const MATRIX_ROLES = ['project-admin', 'project-user']
const CAPABILITIES = ['workspaces', 'ai-agents']

export default function Members() {
  const { name = '' } = useParams()
  const [roles, setRoles] = useState<ProjectRole[]>([])
  const [loading, setLoading] = useState(true)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState('')
  const [subject, setSubject] = useState('')
  const [role, setRole] = useState('project-admin')
  const [matrix, setMatrix] = useState<Record<string, string[]>>({})
  const [matrixDefault, setMatrixDefault] = useState(true)
  const [matrixDirty, setMatrixDirty] = useState(false)

  const refresh = () =>
    Promise.all([
      listProjectRoles(name).then(setRoles),
      getProjectCapabilities(name).then((c) => {
        setMatrix(c.matrix)
        setMatrixDefault(c.is_default)
        setMatrixDirty(false)
      }),
    ])
      .catch((e) => setErr(e.message))
      .finally(() => setLoading(false))

  useEffect(() => {
    refresh()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [name])

  const toggleCap = (r: string, cap: string, on: boolean) => {
    setMatrix((m) => {
      const caps = new Set(m[r] || [])
      if (on) caps.add(cap)
      else caps.delete(cap)
      return { ...m, [r]: CAPABILITIES.filter((c) => caps.has(c)) }
    })
    setMatrixDirty(true)
  }

  const run = async (key: string, fn: () => Promise<unknown>) => {
    setBusy(key)
    setErr('')
    try {
      await fn()
      await refresh()
    } catch (e) {
      setErr((e as Error).message)
    } finally {
      setBusy('')
    }
  }

  if (loading) return <Loading withOverlay description="Loading members" />

  return (
    <div className="page">
      <h2>{name} — members &amp; roles</h2>
      <p style={{ color: 'var(--cds-text-secondary)', margin: '0.5rem 0 1rem' }}>
        Assign project members the admin or user role. A member must already belong to the
        project (its developers group) for a grant to take effect.
      </p>
      {err && (
        <InlineNotification kind="error" title="Error" subtitle={err} lowContrast onCloseButtonClick={() => setErr('')} />
      )}
      <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'flex-end', marginBottom: '1rem' }}>
        <TextInput
          id="grant-subject"
          labelText="Grant role to (email)"
          placeholder="user@example.com"
          value={subject}
          onChange={(e) => setSubject(e.target.value)}
        />
        <Select
          id="grant-role"
          labelText="Role"
          value={role}
          onChange={(e) => setRole(e.target.value)}
        >
          <SelectItem value="project-admin" text="project-admin" />
          <SelectItem value="project-user" text="project-user" />
        </Select>
        <Button
          renderIcon={Add}
          disabled={!subject || busy !== ''}
          onClick={() => run('grant', () => grantProjectRole(name, subject, role).then(() => setSubject('')))}
        >
          Grant
        </Button>
      </div>
      {roles.length === 0 ? (
        <p>No members yet.</p>
      ) : (
        <TableContainer>
          <Table size="lg">
            <TableHead>
              <TableRow>
                <TableHeader>Subject</TableHeader>
                <TableHeader>Role</TableHeader>
                <TableHeader>Granted by</TableHeader>
                <TableHeader>Action</TableHeader>
              </TableRow>
            </TableHead>
            <TableBody>
              {roles.map((r) => (
                <TableRow key={`${r.subject}:${r.role}`}>
                  <TableCell>{r.subject}</TableCell>
                  <TableCell>{r.role}</TableCell>
                  <TableCell>{r.granted_by}</TableCell>
                  <TableCell>
                    <Button
                      kind="danger--ghost"
                      size="sm"
                      renderIcon={TrashCan}
                      disabled={busy !== ''}
                      onClick={() => run(`${r.subject}:${r.role}`, () => revokeProjectRole(name, r.subject, r.role))}
                    >
                      Revoke
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </TableContainer>
      )}

      <h3 style={{ marginTop: '2rem' }}>
        Role capabilities{' '}
        {matrixDefault && !matrixDirty && (
          <Tag type="gray" size="sm">
            defaults
          </Tag>
        )}
      </h3>
      <p style={{ color: 'var(--cds-text-secondary)', margin: '0.5rem 0 1rem' }}>
        What each role may do in this project. Every member starts from the project-user row;
        explicit grants add their role&apos;s capabilities on top. project-admin always keeps all
        capabilities.
      </p>
      <TableContainer>
        <Table size="lg">
          <TableHead>
            <TableRow>
              <TableHeader>Role</TableHeader>
              {CAPABILITIES.map((c) => (
                <TableHeader key={c}>{c}</TableHeader>
              ))}
            </TableRow>
          </TableHead>
          <TableBody>
            {MATRIX_ROLES.map((r) => (
              <TableRow key={r}>
                <TableCell>{r}</TableCell>
                {CAPABILITIES.map((c) => (
                  <TableCell key={c}>
                    <Checkbox
                      id={`cap-${r}-${c}`}
                      labelText=""
                      checked={(matrix[r] || []).includes(c)}
                      disabled={r === 'project-admin'}
                      onChange={(_, { checked }) => toggleCap(r, c, checked)}
                    />
                  </TableCell>
                ))}
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </TableContainer>
      <Button
        style={{ marginTop: '1rem' }}
        disabled={!matrixDirty || busy !== ''}
        onClick={() => run('capabilities', () => putProjectCapabilities(name, matrix))}
      >
        Save capabilities
      </Button>
    </div>
  )
}
