import { Button, Tile } from '@carbon/react'

export default function Login() {
  return (
    <div className="page">
      <Tile style={{ maxWidth: 480, margin: '4rem auto' }}>
        <h2>Secured Dev Workspace</h2>
        <p style={{ margin: '1rem 0' }}>
          Sign in with IBM Verify to access your projects and create secured dev workspaces.
        </p>
        <Button
          onClick={() => {
            window.location.href = '/auth/login'
          }}
        >
          Sign in with IBM Verify
        </Button>
      </Tile>
    </div>
  )
}
