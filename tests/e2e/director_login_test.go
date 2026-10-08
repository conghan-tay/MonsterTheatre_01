package e2e_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/conghan-tay/MonsterTheatre_01/tests/e2e/client"
	"github.com/conghan-tay/MonsterTheatre_01/tests/e2e/fixtures"
)

// H1 (login part): a director logs in and holds a session.
//
// Written in step 3, before the endpoint exists, so it fails with 404 until
// step 4 implements director login.
func TestDirectorLogsIn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	account := fixtures.NewDirector(t)
	director := client.NewDirector(stack.BaseURL)

	resp, err := director.Login(ctx, account.Username, account.Password)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.Status,
		"POST /api/v1/director/login with valid credentials; body: %s", resp.Body)

	cookie, ok := director.Cookie(client.DirectorSessionCookie)
	require.True(t, ok, "login sets the %s cookie", client.DirectorSessionCookie)
	require.NotEmpty(t, cookie.Value)
}
