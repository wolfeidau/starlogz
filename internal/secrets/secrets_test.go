package secrets

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/stretchr/testify/require"
)

type fakeSSM struct {
	pages map[string]*ssm.GetParametersByPathOutput
	err   error
}

func (f *fakeSSM) GetParametersByPath(_ context.Context, in *ssm.GetParametersByPathInput, _ ...func(*ssm.Options)) (*ssm.GetParametersByPathOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.pages[aws.ToString(in.NextToken)], nil
}

func param(name, value string) types.Parameter {
	return types.Parameter{Name: aws.String(name), Value: aws.String(value)}
}

// unsetEnv registers restoration via t.Setenv, then removes the variable.
func unsetEnv(t *testing.T, names ...string) {
	t.Helper()
	for _, n := range names {
		t.Setenv(n, "")
		require.NoError(t, os.Unsetenv(n))
	}
}

func TestLoadEnv_MapsBasenameToUpperCase(t *testing.T) {
	unsetEnv(t, "STARLOGZ_TEST_DATABASE_URL")
	client := &fakeSSM{pages: map[string]*ssm.GetParametersByPathOutput{
		"": {Parameters: []types.Parameter{param("/config/dev/starlogz/starlogz_test_database_url", "postgres://x")}},
	}}

	require.NoError(t, LoadEnv(t.Context(), client, "/config/dev/starlogz"))
	require.Equal(t, "postgres://x", os.Getenv("STARLOGZ_TEST_DATABASE_URL"))
}

func TestLoadEnv_DoesNotOverwriteExisting(t *testing.T) {
	t.Setenv("STARLOGZ_TEST_EXISTING", "explicit")
	client := &fakeSSM{pages: map[string]*ssm.GetParametersByPathOutput{
		"": {Parameters: []types.Parameter{param("/p/starlogz_test_existing", "from-ssm")}},
	}}

	require.NoError(t, LoadEnv(t.Context(), client, "/p"))
	require.Equal(t, "explicit", os.Getenv("STARLOGZ_TEST_EXISTING"))
}

func TestLoadEnv_Paginates(t *testing.T) {
	unsetEnv(t, "STARLOGZ_TEST_ONE", "STARLOGZ_TEST_TWO")
	client := &fakeSSM{pages: map[string]*ssm.GetParametersByPathOutput{
		"": {
			Parameters: []types.Parameter{param("/p/starlogz_test_one", "1")},
			NextToken:  aws.String("page2"),
		},
		"page2": {Parameters: []types.Parameter{param("/p/starlogz_test_two", "2")}},
	}}

	require.NoError(t, LoadEnv(t.Context(), client, "/p"))
	require.Equal(t, "1", os.Getenv("STARLOGZ_TEST_ONE"))
	require.Equal(t, "2", os.Getenv("STARLOGZ_TEST_TWO"))
}

func TestLoadEnv_APIError(t *testing.T) {
	boom := errors.New("boom")

	err := LoadEnv(t.Context(), &fakeSSM{err: boom}, "/p")
	require.ErrorIs(t, err, boom)
	require.ErrorContains(t, err, "/p")
}
