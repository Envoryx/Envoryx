package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"testing"

	"github.com/fxamacker/cbor/v2"
	"github.com/go-webauthn/webauthn/protocol"
)

// softKey is a passkey in software: what a browser and an authenticator answer to the
// ceremonies, enough to run them end to end.
type softKey struct {
	t       *testing.T
	key     *ecdsa.PrivateKey
	id      []byte
	userID  []byte
	counter uint32
}

func newSoftKey(t *testing.T) *softKey {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := make([]byte, 16)
	_, _ = rand.Read(id)
	return &softKey{t: t, key: key, id: id}
}

var b64 = base64.RawURLEncoding

func clientData(kind string, challenge []byte, origin string) []byte {
	b, _ := json.Marshal(map[string]any{"type": kind, "challenge": b64.EncodeToString(challenge), "origin": origin, "crossOrigin": false})
	return b
}

// authData is the authenticator data for rpID; attested adds the credential and its key.
func (k *softKey) authData(rpID string, attested bool) []byte {
	rp := sha256.Sum256([]byte(rpID))
	flags := byte(0x01 | 0x04) // user present, user verified
	if attested {
		flags |= 0x40
	}
	k.counter++
	out := append(rp[:], flags)
	out = binary.BigEndian.AppendUint32(out, k.counter)
	if attested {
		out = append(out, make([]byte, 16)...) // AAGUID
		out = binary.BigEndian.AppendUint16(out, uint16(len(k.id)))
		out = append(out, k.id...)
		pub := k.key.PublicKey
		var x, y [32]byte
		pub.X.FillBytes(x[:])
		pub.Y.FillBytes(y[:])
		cose, err := cbor.Marshal(map[int]any{1: 2, 3: -7, -1: 1, -2: x[:], -3: y[:]})
		if err != nil {
			k.t.Fatal(err)
		}
		out = append(out, cose...)
	}
	return out
}

// create answers navigator.credentials.create at origin.
func (k *softKey) create(opts *protocol.CredentialCreation, origin string) []byte {
	k.userID = []byte(opts.Response.User.ID.(protocol.URLEncodedBase64))
	cd := clientData("webauthn.create", opts.Response.Challenge, origin)
	att, err := cbor.Marshal(map[string]any{"fmt": "none", "attStmt": map[string]any{}, "authData": k.authData(opts.Response.RelyingParty.ID, true)})
	if err != nil {
		k.t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{
		"id": b64.EncodeToString(k.id), "rawId": b64.EncodeToString(k.id), "type": "public-key",
		"response": map[string]any{"clientDataJSON": b64.EncodeToString(cd), "attestationObject": b64.EncodeToString(att)},
	})
	return b
}

// get answers navigator.credentials.get at origin for rpID.
func (k *softKey) get(opts *protocol.CredentialAssertion, rpID, origin string) []byte {
	cd := clientData("webauthn.get", opts.Response.Challenge, origin)
	ad := k.authData(rpID, false)
	sum := sha256.Sum256(cd)
	digest := sha256.Sum256(append(append([]byte{}, ad...), sum[:]...))
	sig, err := ecdsa.SignASN1(rand.Reader, k.key, digest[:])
	if err != nil {
		k.t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{
		"id": b64.EncodeToString(k.id), "rawId": b64.EncodeToString(k.id), "type": "public-key",
		"response": map[string]any{"clientDataJSON": b64.EncodeToString(cd), "authenticatorData": b64.EncodeToString(ad), "signature": b64.EncodeToString(sig), "userHandle": b64.EncodeToString(k.userID)},
	})
	return b
}

func TestPasskeyRegistrationAndSignIn(t *testing.T) {
	svc, st, u, _ := twoFactorUser(t, "developer")
	ctx := context.Background()
	p := Principal{UserID: u.ID, Username: u.Username}
	rp, err := RelyingPartyFor("https://envoryx.example.com")
	if err != nil {
		t.Fatal(err)
	}
	k := newSoftKey(t)

	if _, _, err := svc.BeginPasskeyRegistration(ctx, p, "wrong-password-1", rp); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("registration with a wrong password: %v", err)
	}
	id, creation, err := svc.BeginPasskeyRegistration(ctx, p, "alice-secret-123", rp)
	if err != nil {
		t.Fatal(err)
	}
	sel := creation.Response.AuthenticatorSelection
	if sel.ResidentKey != protocol.ResidentKeyRequirementRequired || sel.UserVerification != protocol.VerificationRequired {
		t.Fatalf("authenticator selection %+v", sel)
	}
	// Another user can't finish the ceremony.
	if _, _, err := svc.FinishPasskeyRegistration(ctx, Principal{UserID: "someone"}, id, "Laptop", k.create(creation, rp.Origin)); err == nil {
		t.Fatal("another user finished the registration")
	}
	id, creation, _ = svc.BeginPasskeyRegistration(ctx, p, "alice-secret-123", rp)
	info, codes, err := svc.FinishPasskeyRegistration(ctx, p, id, " Laptop ", k.create(creation, rp.Origin))
	if err != nil {
		t.Fatal(err)
	}
	if info.Name != "Laptop" || info.RPID != "envoryx.example.com" || len(codes) != recoveryCodeCount {
		t.Fatalf("passkey %+v, %d codes", info, len(codes))
	}

	// The passkey alone signs in, without a user name.
	id, assertion, err := svc.BeginPasskeyLogin(ctx, rp, "")
	if err != nil {
		t.Fatal(err)
	}
	token, user, err := svc.FinishPasskeyLogin(ctx, id, k.get(assertion, rp.ID, rp.Origin), "1.2.3.4", "test")
	if err != nil || user.ID != u.ID {
		t.Fatalf("passkey sign-in: %v", err)
	}
	if pr, err := svc.Validate(ctx, token); err != nil || pr.SessionMethod != MethodPasskey {
		t.Fatalf("session %+v %v", pr, err)
	}
	// A ceremony is answered once.
	if _, _, err := svc.FinishPasskeyLogin(ctx, id, k.get(assertion, rp.ID, rp.Origin), "1.2.3.4", "test"); !errors.Is(err, ErrCeremony) {
		t.Fatalf("a used ceremony: %v", err)
	}

	// As the second step after the password.
	_, _, step, err := svc.PasswordLogin(ctx, "alice", "alice-secret-123", "1.2.3.4", "test")
	if err != nil || step == nil {
		t.Fatalf("PasswordLogin: %+v %v", step, err)
	}
	id, assertion, err = svc.BeginPasskeyLogin(ctx, rp, step.Ticket)
	if err != nil || len(assertion.Response.AllowedCredentials) != 1 {
		t.Fatalf("second step options: %v", err)
	}
	if _, _, err := svc.FinishPasskeyLogin(ctx, id, k.get(assertion, rp.ID, rp.Origin), "1.2.3.4", "test"); err != nil {
		t.Fatalf("second step: %v", err)
	}

	// Another site's answer, or another host name's, doesn't sign in.
	id, assertion, _ = svc.BeginPasskeyLogin(ctx, rp, "")
	if _, _, err := svc.FinishPasskeyLogin(ctx, id, k.get(assertion, rp.ID, "https://evil.example.net"), "1.2.3.4", "test"); !errors.Is(err, ErrPasskey) {
		t.Fatalf("an answer from another origin: %v", err)
	}
	other, _ := RelyingPartyFor("https://other.example.com")
	id, assertion, _ = svc.BeginPasskeyLogin(ctx, other, "")
	if _, _, err := svc.FinishPasskeyLogin(ctx, id, k.get(assertion, other.ID, other.Origin), "1.2.3.4", "test"); !errors.Is(err, ErrPasskey) {
		t.Fatalf("a passkey at another host name: %v", err)
	}

	// A counter that goes back means a copied key.
	k.counter = 0
	id, assertion, _ = svc.BeginPasskeyLogin(ctx, rp, "")
	if _, _, err := svc.FinishPasskeyLogin(ctx, id, k.get(assertion, rp.ID, rp.Origin), "1.2.3.4", "test"); !errors.Is(err, ErrPasskey) {
		t.Fatalf("a counter that went back: %v", err)
	}

	// A disabled user's passkey doesn't sign in.
	k.counter = 100
	if err := st.Users.SetDisabled(ctx, u.ID, true); err != nil {
		t.Fatal(err)
	}
	id, assertion, _ = svc.BeginPasskeyLogin(ctx, rp, "")
	if _, _, err := svc.FinishPasskeyLogin(ctx, id, k.get(assertion, rp.ID, rp.Origin), "1.2.3.4", "test"); err == nil {
		t.Fatal("a disabled user signed in")
	}
	if err := st.Users.SetDisabled(ctx, u.ID, false); err != nil {
		t.Fatal(err)
	}

	// Rename and remove; the last factor takes the recovery codes along.
	if err := svc.RenamePasskey(ctx, p, info.ID, "Work laptop"); err != nil {
		t.Fatal(err)
	}
	if err := svc.RemovePasskey(ctx, Principal{UserID: "someone"}, info.ID, "alice-secret-123"); err == nil {
		t.Fatal("another user removed the passkey")
	}
	if err := svc.RemovePasskey(ctx, p, info.ID, "alice-secret-123"); err != nil {
		t.Fatal(err)
	}
	if f, _ := st.MFA.Factors(ctx, u.ID); f.Any() || f.RecoveryCodes != 0 {
		t.Fatalf("after removing: %+v", f)
	}
	// The removed passkey gets a plain sentence, not the library's wording.
	id, assertion, _ = svc.BeginPasskeyLogin(ctx, rp, "")
	if _, _, err := svc.FinishPasskeyLogin(ctx, id, k.get(assertion, rp.ID, rp.Origin), "1.2.3.5", "test"); !errors.Is(err, ErrUnknownPasskey) || !errors.Is(err, ErrPasskey) {
		t.Fatalf("a removed passkey: %v", err)
	}
}
