package bakerytest_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"

	"github.com/juju/httprequest"
	gc "gopkg.in/check.v1"

	"gopkg.in/macaroon-bakery.v2-unstable/bakery"
	"gopkg.in/macaroon-bakery.v2-unstable/bakery/checkers"
	"gopkg.in/macaroon-bakery.v2-unstable/bakerytest"
	"gopkg.in/macaroon-bakery.v2-unstable/httpbakery"
)

type suite struct {
	client *httpbakery.Client
}

func (s *suite) SetUpTest(c *gc.C) {
	s.client = httpbakery.NewClient()
}

var _ = gc.Suite(&suite{})

func (s *suite) TestDischargerSimple(c *gc.C) {
	d := bakerytest.NewDischarger(nil, nil)
	defer d.Close()

	svc, err := bakery.NewService(bakery.NewServiceParams{
		Location: "here",
		Locator:  d,
	})
	c.Assert(err, gc.IsNil)
	m, err := svc.NewMacaroon(bakery.LatestVersion, []checkers.Caveat{{
		Location:  d.Location(),
		Condition: "something",
	}})

	c.Assert(err, gc.IsNil)
	ms, err := s.client.DischargeAll(m)
	c.Assert(err, gc.IsNil)
	c.Assert(ms, gc.HasLen, 2)

	err = svc.Check(ms, failChecker)
	c.Assert(err, gc.IsNil)
}

var failChecker = bakery.FirstPartyCheckerFunc(func(s string) error {
	return fmt.Errorf("fail %s", s)
})

func (s *suite) TestDischargerTwoLevels(c *gc.C) {
	d1checker := func(cond, arg string) ([]checkers.Caveat, error) {
		if cond != "xtrue" {
			return nil, fmt.Errorf("caveat refused")
		}
		return nil, nil
	}
	d1 := bakerytest.NewDischarger(nil, bakerytest.ConditionParser(d1checker))
	defer d1.Close()
	d2checker := func(cond, arg string) ([]checkers.Caveat, error) {
		return []checkers.Caveat{{
			Location:  d1.Location(),
			Condition: "x" + cond,
		}}, nil
	}
	d2 := bakerytest.NewDischarger(d1, bakerytest.ConditionParser(d2checker))
	defer d2.Close()
	locator := bakery.NewThirdPartyLocatorStore()
	locator.AddInfo(d1.Location(), bakery.ThirdPartyInfo{
		PublicKey: *d1.Service.PublicKey(),
		Version:   bakery.LatestVersion,
	})
	locator.AddInfo(d2.Location(), bakery.ThirdPartyInfo{
		PublicKey: *d2.Service.PublicKey(),
		Version:   bakery.LatestVersion,
	})
	c.Logf("map: %s", locator)
	svc, err := bakery.NewService(bakery.NewServiceParams{
		Location: "here",
		Locator:  locator,
	})
	c.Assert(err, gc.IsNil)
	m, err := svc.NewMacaroon(bakery.LatestVersion, []checkers.Caveat{{
		Location:  d2.Location(),
		Condition: "true",
	}})

	c.Assert(err, gc.IsNil)

	ms, err := s.client.DischargeAll(m)
	c.Assert(err, gc.IsNil)
	c.Assert(ms, gc.HasLen, 3)

	err = svc.Check(ms, failChecker)
	c.Assert(err, gc.IsNil)

	err = svc.AddCaveat(m, checkers.Caveat{
		Location:  d2.Location(),
		Condition: "nope",
	})
	c.Assert(err, gc.IsNil)

	ms, err = s.client.DischargeAll(m)
	c.Assert(err, gc.ErrorMatches, `cannot get discharge from "https://[^"]*": third party refused discharge: cannot discharge: caveat refused`)
	c.Assert(ms, gc.HasLen, 0)
}

func (s *suite) TestInsecureSkipVerifyRestoration(c *gc.C) {
	d1 := bakerytest.NewDischarger(nil, nil)
	d2 := bakerytest.NewDischarger(nil, nil)
	d2.Close()
	c.Assert(http.DefaultTransport.(*http.Transport).TLSClientConfig.InsecureSkipVerify, gc.Equals, true)
	d1.Close()
	c.Assert(http.DefaultTransport.(*http.Transport).TLSClientConfig.InsecureSkipVerify, gc.Equals, false)

	// When InsecureSkipVerify is already true, it should not
	// be restored to false.
	http.DefaultTransport.(*http.Transport).TLSClientConfig.InsecureSkipVerify = true
	d3 := bakerytest.NewDischarger(nil, nil)
	d3.Close()

	c.Assert(http.DefaultTransport.(*http.Transport).TLSClientConfig.InsecureSkipVerify, gc.Equals, true)
}

func (s *suite) TestConcurrentDischargers(c *gc.C) {
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			d := bakerytest.NewDischarger(nil, nil)
			d.Close()
			wg.Done()
		}()
	}
	wg.Wait()
	c.Assert(http.DefaultTransport.(*http.Transport).TLSClientConfig.InsecureSkipVerify, gc.Equals, false)
}

func (s *suite) TestInteractiveDischarger(c *gc.C) {
	var d *bakerytest.InteractiveDischarger
	d = bakerytest.NewInteractiveDischarger(nil, http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			d.FinishInteraction(w, r, []checkers.Caveat{
				checkers.Caveat{
					Condition: "test pass",
				},
			}, nil)
		},
	))
	defer d.Close()

	svc, err := bakery.NewService(bakery.NewServiceParams{
		Location: "here",
		Locator:  d,
	})
	c.Assert(err, gc.IsNil)
	m, err := svc.NewMacaroon(bakery.LatestVersion, []checkers.Caveat{{
		Location:  d.Location(),
		Condition: "something",
	}})

	c.Assert(err, gc.IsNil)
	client := httpbakery.NewClient()
	client.VisitWebPage = func(u *url.URL) error {
		var c httprequest.Client
		return c.Get(u.String(), nil)
	}
	ms, err := client.DischargeAll(m)
	c.Assert(err, gc.IsNil)
	c.Assert(ms, gc.HasLen, 2)

	var r recordingChecker
	err = svc.Check(ms, &r)
	c.Assert(err, gc.IsNil)
	c.Assert(r.caveats, gc.HasLen, 1)
	c.Assert(r.caveats[0], gc.Equals, "test pass")
}

func (s *suite) TestLoginDischargerError(c *gc.C) {
	var d *bakerytest.InteractiveDischarger
	d = bakerytest.NewInteractiveDischarger(nil, http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			d.FinishInteraction(w, r, nil, errors.New("test error"))
		},
	))
	defer d.Close()

	svc, err := bakery.NewService(bakery.NewServiceParams{
		Location: "here",
		Locator:  d,
	})
	c.Assert(err, gc.IsNil)
	m, err := svc.NewMacaroon(bakery.LatestVersion, []checkers.Caveat{{
		Location:  d.Location(),
		Condition: "something",
	}})

	c.Assert(err, gc.IsNil)
	client := httpbakery.NewClient()
	client.VisitWebPage = func(u *url.URL) error {
		c.Logf("visiting %s", u)
		var c httprequest.Client
		return c.Get(u.String(), nil)
	}
	_, err = client.DischargeAll(m)
	c.Assert(err, gc.ErrorMatches, `cannot get discharge from ".*": failed to acquire macaroon after waiting: third party refused discharge: test error`)
}

func (s *suite) TestInteractiveDischargerURL(c *gc.C) {
	var d *bakerytest.InteractiveDischarger
	d = bakerytest.NewInteractiveDischarger(nil, http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, d.URL("/redirect", r), http.StatusFound)
		},
	))
	defer d.Close()
	d.Mux.Handle("/redirect", http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			d.FinishInteraction(w, r, nil, nil)
		},
	))
	svc, err := bakery.NewService(bakery.NewServiceParams{
		Location: "here",
		Locator:  d,
	})
	c.Assert(err, gc.IsNil)
	m, err := svc.NewMacaroon(bakery.LatestVersion, []checkers.Caveat{{
		Location:  d.Location(),
		Condition: "something",
	}})

	c.Assert(err, gc.IsNil)
	client := httpbakery.NewClient()
	client.VisitWebPage = func(u *url.URL) error {
		var c httprequest.Client
		return c.Get(u.String(), nil)
	}
	ms, err := client.DischargeAll(m)
	c.Assert(err, gc.IsNil)
	c.Assert(ms, gc.HasLen, 2)

	err = svc.Check(ms, failChecker)
	c.Assert(err, gc.IsNil)
}

type recordingChecker struct {
	caveats []string
}

func (c *recordingChecker) CheckFirstPartyCaveat(caveat string) error {
	c.caveats = append(c.caveats, caveat)
	return nil
}
