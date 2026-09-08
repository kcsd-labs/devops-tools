package config

import (
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func withEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	for k, v := range kv {
		t.Setenv(k, v)
	}
}

// The password itself is what an operator has; turning it into a hash is this
// program's job, not a step to run beforehand.
func TestPlaintextPasswordIsHashed(t *testing.T) {
	withEnv(t, map[string]string{"DEVOPS_TOOLS_BOOTSTRAP_PASSWORD": "correct horse battery"})
	c := &Config{}
	if err := applyBootstrapPassword(c); err != nil {
		t.Fatal(err)
	}
	hash := c.Auth.Bootstrap.PasswordLogin.PasswordHash
	if hash == "" {
		t.Fatal("no hash was produced")
	}
	// The plaintext must not survive anywhere in the configuration.
	if strings.Contains(hash, "correct horse battery") {
		t.Fatal("the password was stored as given")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte("correct horse battery")); err != nil {
		t.Fatalf("the hash does not match the password: %v", err)
	}
	if err := checkBcryptHash(hash); err != nil {
		t.Fatalf("the hash it produced is one the rest of the program rejects: %v", err)
	}
}

func TestShortPlaintextPasswordIsRefused(t *testing.T) {
	withEnv(t, map[string]string{"DEVOPS_TOOLS_BOOTSTRAP_PASSWORD": "short"})
	if err := applyBootstrapPassword(&Config{}); err == nil {
		t.Fatal("a five-character password was accepted")
	}
}

// Setting both is a mistake worth naming: silently preferring one leaves the
// person who set the other unable to sign in and with nothing to go on.
func TestBothPasswordFormsIsAnError(t *testing.T) {
	withEnv(t, map[string]string{"DEVOPS_TOOLS_BOOTSTRAP_PASSWORD": "correct horse battery"})
	c := &Config{}
	c.Auth.Bootstrap.PasswordLogin.PasswordHash = "$2a$10$g8J.N1mZm6FUHfQR5FMYButGpMFIZeYtnd4aJd6sp.gXQQ.LQmUDq"
	err := applyBootstrapPassword(c)
	if err == nil {
		t.Fatal("both forms were accepted")
	}
	if !strings.Contains(err.Error(), "one or the other") {
		t.Fatalf("the error does not say what to do: %v", err)
	}
}

// Without the variable nothing happens: a hash written in the YAML stays.
func TestNoPlaintextLeavesTheHashAlone(t *testing.T) {
	c := &Config{}
	const h = "$2a$10$g8J.N1mZm6FUHfQR5FMYButGpMFIZeYtnd4aJd6sp.gXQQ.LQmUDq"
	c.Auth.Bootstrap.PasswordLogin.PasswordHash = h
	if err := applyBootstrapPassword(c); err != nil {
		t.Fatal(err)
	}
	if c.Auth.Bootstrap.PasswordLogin.PasswordHash != h {
		t.Fatal("the configured hash was replaced")
	}
}
