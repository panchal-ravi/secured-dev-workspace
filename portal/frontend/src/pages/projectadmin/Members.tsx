import { useEffect, useState } from 'react'
import { useParams } from 'react-router-dom'
import {
  Button,
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
  TextInput,
} from '@carbon/react'
import { Add, TrashCan } from '@carbon/icons-react'
import { listProjectRoles, grantProjectRole, revokeProjectRole, ProjectRole } from '../../api/client'

export default function Members() {
  const { name = '' } = useParams()
  const [roles, setRoles] = useState<ProjectRole[]>([])
  const [loading, setLoading] = useState(true)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState('')
  const [subject, setSubject] = useState('')
  const [role, setRole] = useState('project-admin')

  const refresh = () =>
    listProjectRoles(name)
      .then(setRoles)
      .catch((e) => setErr(e.message))
      .finally(() => setLoading(false))

  useEffect(() => {
    refresh()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [name])

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
        Assign project members the admin or developer role. A member must already belong to the
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
          <SelectItem value="project-developer" text="project-developer" />
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
    </div>
  )
}
