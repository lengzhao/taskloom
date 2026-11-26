package security

import "time"

// MaxCredentialLifetime bounds every issued credential, for people and Agents
// alike. Administrators pick a shorter lifetime when they want one.
const MaxCredentialLifetime = 360 * 24 * time.Hour
