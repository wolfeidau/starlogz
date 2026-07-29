package server

import "github.com/wolfeidau/starlogz/internal/store"

type operatorAuthorizer map[int64]struct{}

func newOperatorAuthorizer(githubIDs []int64) operatorAuthorizer {
	authorizer := make(operatorAuthorizer, len(githubIDs))
	for _, id := range githubIDs {
		if id > 0 {
			authorizer[id] = struct{}{}
		}
	}
	return authorizer
}

func (a operatorAuthorizer) Allows(user *store.User) bool {
	if user == nil {
		return false
	}
	_, ok := a[user.GitHubID]
	return ok
}
