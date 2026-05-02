package store

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func testKey() [32]byte {
	var k [32]byte
	copy(k[:], "test-encryption-key-0123456789ab")
	return k
}

func TestEncryptor_SealOpen_RoundTrip(t *testing.T) {
	enc := NewEncryptor(testKey())
	plaintext := "github_app_access_token_abc123"

	ct, err := enc.Seal(plaintext)
	require.NoError(t, err)
	require.NotEmpty(t, ct)

	got, err := enc.Open(ct)
	require.NoError(t, err)
	require.Equal(t, plaintext, got)
}

func TestEncryptor_Seal_ProducesUniqueNonces(t *testing.T) {
	enc := NewEncryptor(testKey())
	ct1, err := enc.Seal("same plaintext")
	require.NoError(t, err)
	ct2, err := enc.Seal("same plaintext")
	require.NoError(t, err)
	require.NotEqual(t, ct1, ct2, "each Seal call must use a fresh random nonce")
}

func TestEncryptor_Open_WrongKey(t *testing.T) {
	enc1 := NewEncryptor(testKey())
	var k2 [32]byte
	copy(k2[:], "different-key-zyxwvutsrqponmlkj")
	enc2 := NewEncryptor(k2)

	ct, err := enc1.Seal("secret")
	require.NoError(t, err)

	_, err = enc2.Open(ct)
	require.Error(t, err)
}

func TestEncryptor_Open_ShortCiphertext(t *testing.T) {
	enc := NewEncryptor(testKey())
	_, err := enc.Open([]byte("too short"))
	require.Error(t, err)
}

func TestEncryptor_Open_EmptyCiphertext(t *testing.T) {
	enc := NewEncryptor(testKey())
	_, err := enc.Open([]byte{})
	require.Error(t, err)
}
