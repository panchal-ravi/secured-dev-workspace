package hashistack

// TLSOptions controls how the portal's HashiStack clients verify the server's
// certificate. The stack's TLS is self-signed, so the PoC default is SkipVerify
// (like the CLIs). Production sets SkipVerify=false and supplies CACertPath (the
// stack CA) so the connection is actually authenticated.
type TLSOptions struct {
	CACertPath string // PEM file path; ignored when SkipVerify is true
	SkipVerify bool
}
