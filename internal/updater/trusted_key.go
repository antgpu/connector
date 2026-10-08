package updater

// Set by the release build from the reviewed, repository-owned public key.
// Empty keys fail closed; developer builds cannot consume unsigned fleet updates.
var TrustedPublicKey string
