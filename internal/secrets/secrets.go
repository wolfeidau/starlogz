package secrets

import (
	"context"
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

// LoadEnv sets each SSM parameter under ssmPath as an upper-cased environment variable unless already set.
func LoadEnv(ctx context.Context, client ssm.GetParametersByPathAPIClient, ssmPath string) error {
	pager := ssm.NewGetParametersByPathPaginator(client, &ssm.GetParametersByPathInput{
		Path:           aws.String(ssmPath),
		WithDecryption: aws.Bool(true),
		Recursive:      aws.Bool(false),
	})
	for pager.HasMorePages() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("failed to load SSM parameters from %s: %w", ssmPath, err)
		}
		for _, p := range page.Parameters {
			name := strings.ToUpper(path.Base(aws.ToString(p.Name)))
			if _, ok := os.LookupEnv(name); ok {
				continue
			}
			if err := os.Setenv(name, aws.ToString(p.Value)); err != nil {
				return fmt.Errorf("failed to set %s: %w", name, err)
			}
		}
	}
	return nil
}
