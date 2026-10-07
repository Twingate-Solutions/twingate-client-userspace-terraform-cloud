package provider

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/echoprovider"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
	"github.com/hashicorp/terraform-plugin-testing/tfversion"

	"github.com/Twingate-Solutions/twingate-client-userspace-terraform-cloud/internal/supervisor"
	"github.com/Twingate-Solutions/twingate-client-userspace-terraform-cloud/internal/testutil"
)

func TestMain(m *testing.M) {
	testutil.MaybeRunFakeTwingated()
	// Open re-executes this test binary as the supervisor.
	if os.Getenv(supervisor.ModeEnv) != "" {
		if err := supervisor.Main(); err != nil {
			log.Fatal(err)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func factories() map[string]func() (tfprotov6.ProviderServer, error) {
	return map[string]func() (tfprotov6.ProviderServer, error){
		"twingatetunnel": providerserver.NewProtocol6WithError(New("test")()),
		"echo":           echoprovider.NewProviderServer(),
	}
}

// supervisorsRunning counts live supervisor processes started from this binary.
func supervisorsRunning() int {
	ents, _ := os.ReadDir("/proc")
	n := 0
	for _, e := range ents {
		env, err := os.ReadFile(filepath.Join("/proc", e.Name(), "environ"))
		if err != nil || !bytes.Contains(env, []byte(supervisor.ModeEnv+"=")) {
			continue
		}
		stat, _ := os.ReadFile(filepath.Join("/proc", e.Name(), "stat"))
		if i := bytes.LastIndexByte(stat, ')'); i >= 0 && i+2 < len(stat) && stat[i+2] == 'Z' {
			continue
		}
		n++
	}
	return n
}

// TestAccTCP runs plan and apply with the real Terraform CLI and a fake
// twingated, then checks that every supervisor was shut down.
func TestAccTCP(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("set TF_ACC=1 to run acceptance tests")
	}
	echo := testutil.EchoServer(t)
	fake := testutil.WriteFakeTwingated(t)
	t.Setenv("TWINGATE_SERVICE_KEY", `{"private_key":"not-a-real-key"}`)

	config := fmt.Sprintf(`
provider "twingatetunnel" {
  client_path = %q
  log_dir     = %q
}

ephemeral "twingatetunnel_tcp" "db" {
  destination   = %q
  ready_timeout = "20s"
}

ephemeral "twingatetunnel_http_proxy" "px" {}

provider "echo" {
  data = {
    host  = ephemeral.twingatetunnel_tcp.db.local_host
    port  = ephemeral.twingatetunnel_tcp.db.local_port
    proxy = ephemeral.twingatetunnel_http_proxy.px.proxy_url
  }
}

resource "echo" "out" {}
`, fake, t.TempDir(), echo)

	resource.UnitTest(t, resource.TestCase{
		TerraformVersionChecks: []tfversion.TerraformVersionCheck{
			tfversion.SkipBelow(tfversion.Version1_10_0),
		},
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{{
			Config: config,
			ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue("echo.out", tfjsonpath.New("data").AtMapKey("host"), knownvalue.StringExact("127.0.0.1")),
				statecheck.ExpectKnownValue("echo.out", tfjsonpath.New("data").AtMapKey("port"), knownvalue.NotNull()),
				statecheck.ExpectKnownValue("echo.out", tfjsonpath.New("data").AtMapKey("proxy"),
					knownvalue.StringRegexp(regexp.MustCompile(`^http://127\.0\.0\.1:\d+$`))),
			},
		}},
	})

	deadline := time.Now().Add(10 * time.Second)
	for supervisorsRunning() > 0 && time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
	}
	if n := supervisorsRunning(); n > 0 {
		t.Fatalf("%d supervisor process(es) left running", n)
	}
}

func TestAccMissingServiceKey(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("set TF_ACC=1 to run acceptance tests")
	}
	t.Setenv("TWINGATE_SERVICE_KEY", "")
	resource.UnitTest(t, resource.TestCase{
		TerraformVersionChecks: []tfversion.TerraformVersionCheck{
			tfversion.SkipBelow(tfversion.Version1_10_0),
		},
		ProtoV6ProviderFactories: factories(),
		Steps: []resource.TestStep{{
			Config: `
provider "twingatetunnel" {
  client_path = "/nonexistent"
}
ephemeral "twingatetunnel_tcp" "db" {
  destination = "db.internal:5432"
}
provider "echo" {
  data = ephemeral.twingatetunnel_tcp.db.local_port
}
resource "echo" "out" {}
`,
			ExpectError: regexp.MustCompile(`Missing Twingate service key`),
		}},
	})
}
