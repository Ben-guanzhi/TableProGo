// Package totp implements RFC 6238 time-based one-time passwords.
//
// Why a hand-rolled implementation in a kernel-adjacent package: the server mode
// needs a second factor, and the alternatives were a dependency whose verification
// story we cannot read, or "TOTP-ish" code that scans a QR, accepts some numbers and
// rejects others. This is the third option: 120 lines whose correctness is pinned by
// the RFC's OWN test vectors (Appendix B) plus the skew and reuse rules that decide
// whether the factor is actually a factor.
//
// What the design refuses to do:
//
//   - It never accepts a code twice. A TOTP code is valid for a 30-second window, so
//     accepting the same code twice inside that window turns "something you have" into
//     "something you saw once" — a replayed code from a shoulder-surfed screen would
//     pass. LastUsedStep remembers the last accepted step.
//   - It requires an explicit skew allowance. Default ±1 step (30s either way) is
//     enough for clock drift and small enough that a stolen code is not usable for
//     long; a caller that wants stricter says so.
//   - It verifies with a constant-time comparison, because a length-changing early
//     return on a 6-digit string is a timing oracle worth about 20 guesses.
package totp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Digits is the standard code length (RFC 6238 §5.3 recommends 6).
const Digits = 6

// Period is the standard time step in seconds.
const Period = 30

// Secret is a shared key. It is the base32 form users paste into an authenticator,
// stored WITHOUT padding because that is what every authenticator app accepts.
type Secret struct {
	raw []byte
}

// GenerateSecret creates a new random secret (160 bits, the RFC 4226 §4 recommendation
// for HMAC-SHA1).
func GenerateSecret() (Secret, error) {
	buf := make([]byte, 20)
	if _, err := rand.Read(buf); err != nil {
		return Secret{}, err
	}
	return Secret{raw: buf}, nil
}

// SecretFromBase32 parses an existing secret so a user can re-enrol the same key on a
// second device.
func SecretFromBase32(s string) (Secret, error) {
	// Authenticators show the key in groups separated by spaces; users paste it that way.
	clean := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(s), " ", ""))
	clean = strings.TrimRight(clean, "=")
	raw, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(clean)
	if err != nil {
		return Secret{}, fmt.Errorf("totp: 密钥不是合法的 base32: %w", err)
	}
	if len(raw) < 10 {
		return Secret{}, fmt.Errorf("totp: 密钥太短（至少 10 字节）")
	}
	return Secret{raw: raw}, nil
}

// String returns the unpadded base32 form.
func (s Secret) String() string {
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(s.raw)
}

// Empty reports whether the secret was never set.
func (s Secret) Empty() bool { return len(s.raw) == 0 }

// Step is the RFC 6238 time step for an instant.
func Step(t time.Time) int64 { return t.Unix() / Period }

// CodeAt computes the code for one step (RFC 4226 HOTP with the step as counter).
func (s Secret) CodeAt(step int64) string {
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(step))
	mac := hmac.New(sha1.New, s.raw)
	mac.Write(counter[:])
	sum := mac.Sum(nil)
	// Dynamic truncation, RFC 4226 §5.4.
	offset := sum[len(sum)-1] & 0x0f
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	mod := uint32(1)
	for i := 0; i < Digits; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", Digits, value%mod)
}

// Code computes the code for an instant.
func (s Secret) Code(t time.Time) string { return s.CodeAt(Step(t)) }

// Options configures verification.
type Options struct {
	// Skew is how many steps either side of now are accepted. Zero means "this step
	// only"; callers normally pass 1.
	Skew int
	// LastUsedStep is the last step this user's code was accepted for. A code at or
	// before it is refused, which is what stops a replay. Pass 0 to disable reuse
	// checking (first enrolment).
	LastUsedStep int64
}

// Verify checks a submitted code and returns the step it was valid for.
//
// ok=false with a non-empty error is a malformed input; ok=false with an empty error
// is simply a wrong code. The distinction matters for the caller's logging: a
// malformed code is a bug or an attack, a wrong one is usually a typo.
func (s Secret) Verify(code string, now time.Time, opts Options) (step int64, ok bool, err error) {
	clean := strings.TrimSpace(code)
	// Accept "123 456" and "123-456": users copy from apps that group the digits.
	clean = strings.NewReplacer(" ", "", "-", "").Replace(clean)
	if len(clean) != Digits {
		return 0, false, fmt.Errorf("totp: 验证码应为 %d 位数字", Digits)
	}
	for _, r := range clean {
		if r < '0' || r > '9' {
			return 0, false, fmt.Errorf("totp: 验证码只能包含数字")
		}
	}
	current := Step(now)
	skew := opts.Skew
	if skew < 0 {
		skew = 0
	}
	for delta := -int64(skew); delta <= int64(skew); delta++ {
		candidate := current + delta
		if opts.LastUsedStep > 0 && candidate <= opts.LastUsedStep {
			// Already used (or older than one we have used): refuse rather than let a
			// replayed code through inside its own validity window.
			continue
		}
		if subtle.ConstantTimeCompare([]byte(s.CodeAt(candidate)), []byte(clean)) == 1 {
			return candidate, true, nil
		}
	}
	return 0, false, nil
}

// ProvisioningURI builds the otpauth:// URI an authenticator scans.
//
// issuer and account appear in the user's app; the secret is the only thing that
// matters cryptographically. The URI is returned as a string so the caller can render
// a QR code or print it — this package deliberately does not draw one.
func (s Secret) ProvisioningURI(issuer, account string) string {
	label := url.PathEscape(issuer + ":" + account)
	q := url.Values{}
	q.Set("secret", s.String())
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", fmt.Sprint(Digits))
	q.Set("period", fmt.Sprint(Period))
	return "otpauth://totp/" + label + "?" + q.Encode()
}
