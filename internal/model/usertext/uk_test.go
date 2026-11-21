package usertext

import "testing"

// The texts below are stored in journals and exports: they must stay byte for byte as they are.
func TestComposedTexts(t *testing.T) {
	cases := map[string][2]string{
		"deferred quota": {DeferredQuota, "\u0412\u0456\u0434\u043a\u043b\u0430\u0434\u0435\u043d\u043e: \u0432\u0438\u0447\u0435\u0440\u043f\u0430\u043d\u043e \u0434\u043e\u0431\u043e\u0432\u0438\u0439 \u043b\u0456\u043c\u0456\u0442"},
		"attempts cell":  {AttemptsCell(3), "\u0441\u043f\u0440\u043e\u0431: 3"},
		"screen name":    {LiveDisplayName(2), "\u0415\u043a\u0440\u0430\u043d 2"},
		"import suffix":  {ImportedNameSuffix(4), " (\u0456\u043c\u043f\u043e\u0440\u0442 4)"},
		"test mail html": {TestMailHTML("x"), "<p>\u0426\u0435 \u0442\u0435\u0441\u0442\u043e\u0432\u0438\u0439 \u043b\u0438\u0441\u0442: \u043d\u0430\u043b\u0430\u0448\u0442\u0443\u0432\u0430\u043d\u043d\u044f SMTP x \u043f\u0440\u0430\u0446\u044e\u044e\u0442\u044c.</p>"},
	}
	for name, c := range cases {
		if c[0] != c[1] {
			t.Errorf("%s = %q, want %q", name, c[0], c[1])
		}
	}
}

func TestDeferredHasPrefix(t *testing.T) {
	for _, s := range []string{DeferredQuota, DeferredRate} {
		if len(s) <= len(DeferredPrefix) || s[:len(DeferredPrefix)] != DeferredPrefix {
			t.Errorf("%q does not start with the deferral prefix", s)
		}
	}
}
