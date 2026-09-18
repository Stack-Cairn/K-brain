package browser

import "testing"

func TestSetDriver(t *testing.T) {
	defer func() { Driver = DriverRod }()
	t.Setenv("K_BRAIN_BROWSER_DRIVER", "")
	SetDriver(DriverChromedp)
	if Driver != DriverChromedp {
		t.Fatalf("got %q", Driver)
	}
	SetDriver(DriverRod)
	if Driver != DriverRod {
		t.Fatalf("got %q", Driver)
	}
	SetDriver("bogus")
	if Driver != DriverRod {
		t.Fatalf("bogus driver must be ignored, got %q", Driver)
	}
}

func TestSetDriverEnvPinWins(t *testing.T) {
	defer func() { Driver = DriverRod }()
	t.Setenv("K_BRAIN_BROWSER_DRIVER", "chromedp")
	SetDriver(DriverRod)
	if Driver != DriverRod {

		t.Fatalf("pin must make SetDriver a no-op, got %q", Driver)
	}
}
