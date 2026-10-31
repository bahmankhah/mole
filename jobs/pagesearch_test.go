package jobs

import (
	"reflect"
	"strings"
	"testing"
)

func TestPageSearchCondition(t *testing.T) {
	if cond, args := pageSearchCondition("   "); cond != "" || args != nil {
		t.Fatalf("empty query = %q %v", cond, args)
	}

	cond, args := pageSearchCondition("iphone 15")
	if strings.Count(cond, " AND ") != 1 || strings.Count(cond, "LIKE ?") != 6 {
		t.Errorf("two ASCII words: %s", cond)
	}
	if args[0] != "%iphone%" || args[3] != "%15%" {
		t.Errorf("args = %v", args)
	}

	// LIKE wildcards in the query are literal.
	_, args = pageSearchCondition(`50%_off\`)
	if args[0] != `%50\%\_off\\%` {
		t.Errorf("escaped = %v", args[0])
	}

	// Persian: both letter forms, each also percent-encoded for URLs.
	cond, args = pageSearchCondition("دیفرانسیل")
	if strings.Count(cond, "LIKE ?") != 10 {
		t.Errorf("persian: %s", cond)
	}
	enc := likePattern("%D8%AF%DB%8C%D9%81%D8%B1%D8%A7%D9%86%D8%B3%DB%8C%D9%84")
	want := []interface{}{"%دیفرانسیل%", "%دیفرانسیل%", "%دیفرانسیل%", enc, enc}
	if !reflect.DeepEqual(args[:5], want) {
		t.Errorf("persian args = %v", args[:5])
	}
	if args[5] != "%ديفرانسيل%" {
		t.Errorf("arabic form = %v", args[5])
	}

	// Arabic letters in the query find Persian titles too.
	_, args = pageSearchCondition("كيف")
	if args[0] != "%کیف%" {
		t.Errorf("normalized = %v", args[0])
	}
}
