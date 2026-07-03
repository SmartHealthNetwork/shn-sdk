// Package accounts is the SHN developer-account client surface: the
// unauthenticated discovery fetchers ({accounts}/cli-config + OIDC
// discovery), the OAuth 2.1 loopback-PKCE sign-in flow against Cognito,
// token refresh, and the authenticated Accounts-service API client
// (two-step client registration, list, revoke).
//
// It is consumed by the shn CLI (sdk/cmd/shn) and the SHN Kit daemon
// (shnkitd) — one implementation of the sign-in + registration arc, two
// frontends. Dep-purity: stdlib + shnsdk only; never the substrate's
// internal packages.
package accounts
