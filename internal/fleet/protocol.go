// Package fleet connects an instance to the fleet manager of its hoster
// (github.com/envoryx/fleet): the instance enrolls once with a one-time token, then
// keeps a WebSocket open to the manager, which pushes the instance's plan and gets the
// instance's state back. The instance dials out, so it needs no open port.
//
// The protocol is mirrored in the fleet manager (PROTOCOL.md there); a change goes into
// both.
package fleet

import (
	"crypto/ed25519"
	"encoding/json"
)

// Paths of the manager's agent API.
const (
	enrollPath  = "/api/agent/v1/enroll"
	connectPath = "/api/agent/v1/connect"
)

// HeaderInstance names the fleet ID of the instance on the WebSocket request.
const HeaderInstance = "X-Envoryx-Instance"

// CloseRevoked is the WebSocket close code of an instance the manager removed; the
// agent stops connecting.
const CloseRevoked = 4001

// enrollRequest is what an instance sends once, with the token the hoster gave it.
type enrollRequest struct {
	Token string `json:"token"`
	// PublicKey is the instance's Ed25519 key; the manager checks every connection
	// against it.
	PublicKey  []byte `json:"publicKey"`
	InstanceID string `json:"instanceId"`
	Version    string `json:"version"`
	Hostname   string `json:"hostname"`
}

// enrollResponse names the instance in the fleet.
type enrollResponse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// message is every frame on the WebSocket; Type decides which fields are set.
//
//	manager → instance: challenge (Nonce), welcome (Name), plan (Plan, null for none)
//	instance → manager: auth (Signature over the nonce), status (Status)
type message struct {
	Type      string          `json:"type"`
	Nonce     []byte          `json:"nonce,omitempty"`
	Signature []byte          `json:"signature,omitempty"`
	Name      string          `json:"name,omitempty"`
	Plan      json.RawMessage `json:"plan,omitempty"`
	Status    *Status         `json:"status,omitempty"`
}

// Status is what an instance reports about itself, at connect and every minute.
type Status struct {
	Version    string `json:"version"`
	InstanceID string `json:"instanceId"`
	Hostname   string `json:"hostname"`
	// PlanName is the plan the instance runs with; PlanError why the last plan from
	// the manager was not taken over.
	PlanName  string `json:"planName,omitempty"`
	PlanError string `json:"planError,omitempty"`
	Usage     Usage  `json:"usage"`
	// Warnings are the instance's open problems (failing projects, low disk space).
	Warnings []string `json:"warnings,omitempty"`
}

// Usage is how much of its plan an instance uses.
type Usage struct {
	Projects        int   `json:"projects"`
	RunningProjects int   `json:"runningProjects"`
	Users           int   `json:"users"`
	DiskBytes       int64 `json:"diskBytes"`
}

// challengeText is what the instance signs to prove it holds its key: the nonce in a
// context of its own, so the signature can't be taken for anything else.
func challengeText(nonce []byte) []byte {
	return append([]byte("envoryx-fleet-auth\n"), nonce...)
}

// signChallenge signs a nonce with the instance's key.
func signChallenge(key ed25519.PrivateKey, nonce []byte) []byte {
	return ed25519.Sign(key, challengeText(nonce))
}
