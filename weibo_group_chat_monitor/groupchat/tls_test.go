package groupchat

import "testing"

func TestNewWeiboIMTLSConfigAddsIntermediateWithoutDisablingVerification(t *testing.T) {
	config, err := NewWeiboIMTLSConfig(false)
	if err != nil {
		t.Fatalf("NewWeiboIMTLSConfig failed: %v", err)
	}
	if config.InsecureSkipVerify {
		t.Fatal("strict config unexpectedly disables verification")
	}
	if config.RootCAs == nil {
		t.Fatal("strict config must include root pool")
	}
}
