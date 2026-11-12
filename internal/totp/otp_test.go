package totp

// Correctness is pinned by the RFC's OWN vectors (Appendix B): a TOTP implementation
// that "looks right" will accept a code from the wrong window or truncate wrongly, and
// nothing but the published vectors catches that.
//
// RFC 6238 Appendix B uses 8 digits; this package fixes 6 (the RFC's recommended
// length and what every authenticator shows), so each vector's expected value is the
// 8-digit value's last 6 digits — which is exactly how truncation works.

import (
	"testing"
	"time"
)

// seed32 is the RFC's ASCII seed "12345678901234567890".
const seed32 = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"

func mustSecret(t *testing.T, b32 string) Secret {
	t.Helper()
	s, err := SecretFromBase32(b32)
	if err != nil {
		t.Fatalf("secret: %v", err)
	}
	return s
}

func TestRFC6238Vectors(t *testing.T) {
	// The canonical table: T0=0, X=30. Expected values are the RFC's 8-digit outputs
	// truncated to 6.
	cases := []struct {
		unix int64
		want string
	}{
		{59, "287082"},          // 94287082 -> 287082
		{1111111109, "081804"},  // 07081804 -> 081804
		{1111111111, "050471"},  // 14050471 -> 050471
		{1234567890, "005924"},  // 89005924 -> 005924
		{2000000000, "279037"},  // 69279037 -> 279037
		{20000000000, "353130"}, // 65353130 -> 353130
	}
	secret := mustSecret(t, seed32)
	for _, c := range cases {
		got := secret.CodeAt(c.unix / Period)
		if got != c.want {
			t.Fatalf("t=%d: got %s, want %s", c.unix, got, c.want)
		}
		// And through the time-based entry point, which is what callers use.
		if got := secret.Code(time.Unix(c.unix, 0)); got != c.want {
			t.Fatalf("t=%d via Code: got %s, want %s", c.unix, got, c.want)
		}
	}
}

func TestVerifyAcceptsTheCurrentStep(t *testing.T) {
	secret := mustSecret(t, seed32)
	now := time.Unix(1111111109, 0)
	code := secret.Code(now)
	step, ok, err := secret.Verify(code, now, Options{Skew: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatalf("the current code must verify (code=%s)", code)
	}
	if step != Step(now) {
		t.Fatalf("step = %d, want %d", step, Step(now))
	}
}

func TestVerifyHonoursSkewAndRefusesBeyondIt(t *testing.T) {
	secret := mustSecret(t, seed32)
	now := time.Unix(1111111109, 0)
	previous := secret.CodeAt(Step(now) - 1)
	next := secret.CodeAt(Step(now) + 1)

	// Without skew, a neighbouring step is wrong.
	if _, ok, _ := secret.Verify(previous, now, Options{}); ok {
		t.Fatal("the previous step must not verify with no skew")
	}
	// With skew 1 both neighbours verify: that is the clock-drift allowance.
	if _, ok, _ := secret.Verify(previous, now, Options{Skew: 1}); !ok {
		t.Fatal("the previous step must verify with skew 1")
	}
	if _, ok, _ := secret.Verify(next, now, Options{Skew: 1}); !ok {
		t.Fatal("the next step must verify with skew 1")
	}
	// Two steps away is beyond the allowance.
	twoBack := secret.CodeAt(Step(now) - 2)
	if _, ok, _ := secret.Verify(twoBack, now, Options{Skew: 1}); ok {
		t.Fatal("a code two steps away must be refused")
	}
}

func TestVerifyRefusesAReplay(t *testing.T) {
	// The point of the last-used step: a code is valid for 30 seconds, so accepting it
	// twice turns "something you have" into "something you saw once".
	secret := mustSecret(t, seed32)
	now := time.Unix(1111111109, 0)
	code := secret.Code(now)

	step, ok, _ := secret.Verify(code, now, Options{Skew: 1})
	if !ok {
		t.Fatal("first use must verify")
	}
	if _, ok, _ := secret.Verify(code, now, Options{Skew: 1, LastUsedStep: step}); ok {
		t.Fatal("a replayed code must be refused")
	}
	// The NEXT step's code still works: only the used one is burnt.
	later := now.Add(Period * time.Second)
	if _, ok, _ := secret.Verify(secret.Code(later), later, Options{Skew: 1, LastUsedStep: step}); !ok {
		t.Fatal("a fresh code after the used step must verify")
	}
}

func TestVerifyRejectsMalformedInput(t *testing.T) {
	secret := mustSecret(t, seed32)
	now := time.Unix(1111111109, 0)
	for _, bad := range []string{"", "12345", "1234567", "abcdef", "12 34 5"} {
		if _, ok, err := secret.Verify(bad, now, Options{}); ok || err == nil {
			t.Fatalf("%q must be refused as malformed (ok=%v err=%v)", bad, ok, err)
		}
	}
	// A well-formed but wrong code is not malformed: no error, just no match.
	if _, ok, err := secret.Verify("000000", now, Options{}); ok || err != nil {
		t.Fatalf("a wrong code should be ok=false err=nil (ok=%v err=%v)", ok, err)
	}
}

func TestVerifyAcceptsGroupedDigits(t *testing.T) {
	// Authenticators display "123 456"; users type it that way.
	secret := mustSecret(t, seed32)
	now := time.Unix(1111111109, 0)
	code := secret.Code(now)
	grouped := code[:3] + " " + code[3:]
	if _, ok, _ := secret.Verify(grouped, now, Options{}); !ok {
		t.Fatalf("grouped code %q must verify", grouped)
	}
}

func TestSecretRoundTripAndRejectsGarbage(t *testing.T) {
	secret, err := GenerateSecret()
	if err != nil {
		t.Fatal(err)
	}
	if secret.Empty() {
		t.Fatal("a generated secret must not be empty")
	}
	back, err := SecretFromBase32(secret.String())
	if err != nil {
		t.Fatal(err)
	}
	if back.String() != secret.String() {
		t.Fatalf("round trip changed the secret: %s vs %s", back.String(), secret.String())
	}
	// Padding and spacing are tolerated: users copy whatever their app shows.
	if _, err := SecretFromBase32(" " + secret.String() + " "); err != nil {
		t.Fatalf("spaces must be tolerated: %v", err)
	}
	if _, err := SecretFromBase32("not-base32!!"); err == nil {
		t.Fatal("garbage must be refused")
	}
	if _, err := SecretFromBase32("MFRGG"); err == nil {
		t.Fatal("a too-short key must be refused")
	}
}

func TestProvisioningURICarriesWhatAnAppNeeds(t *testing.T) {
	secret := mustSecret(t, seed32)
	uri := secret.ProvisioningURI("TablePro Go", "alice@example.com")
	for _, want := range []string{
		"otpauth://totp/",
		"secret=" + secret.String(),
		"issuer=TablePro+Go",
		"digits=6",
		"period=30",
		"algorithm=SHA1",
	} {
		if !contains(uri, want) {
			t.Fatalf("URI missing %q: %s", want, uri)
		}
	}
	// The label is `issuer:account` with the parts path-escaped: a space becomes %20,
	// while ':' and '@' are legal in a path segment and stay literal. That is the shape
	// authenticators parse (issuer before the colon), and it keeps the account's own
	// characters intact.
	if !contains(uri, "TablePro%20Go:alice@example.com") {
		t.Fatalf("label not escaped as expected: %s", uri)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
