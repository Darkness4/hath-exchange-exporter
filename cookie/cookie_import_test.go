package cookie_test

import (
	"net/http/cookiejar"
	"net/url"
	"testing"

	"github.com/Darkness4/hath-exchange-exporter/cookie"
	"github.com/stretchr/testify/require"
)

func TestParseFromFile(t *testing.T) {
	// Act
	jar, err := cookiejar.New(&cookiejar.Options{})
	require.NoError(t, err)
	err = cookie.ParseFromFile(jar, "fixtures/fixture.txt")
	require.NoError(t, err)

	url, err := url.Parse("https://e-hentai.org/exchange.php")
	require.NoError(t, err)
	cookies := jar.Cookies(url)
	expected := []string{
		"ipb_member_id",
		"ipb_pass_hash",
		"sk",
		"hath_perks",
		"ipb_session_id",
		"cf_clearance",
	}

expectLoop:
	for _, test := range expected {
		for _, cookie := range cookies {
			if cookie.Name == test {
				continue expectLoop
			}
		}
		t.Errorf("cookie %s not found", test)
	}
}
