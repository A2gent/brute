package speechengine

import (
	"strings"
	"testing"
)

func TestNormalizeRussianText(t *testing.T) {
	t.Setenv("AAGENT_DATA_PATH", t.TempDir())
	cases := map[string]string{
		"В 2024 году у нас было 125 клиентов и 3,5 процента роста": "В две тысячи двадцать четвёртом году у нас было сто двадцать пять клиентов и три целых пять десятых процента роста",
		"deploy на production через GitHub Actions":                "дипл+ой на прод+акшен через г+итхаб +экшенс",
		"рост 21%":                            "рост двадцать один процент",
		"1 500 рублей":                        "одна тысяча пятьсот рублей",
		"вызов API":                           "вызов эй пи +ай",
		"suggest и commits в pre-commit hook": "сэдж+ест и кам+иты в при-кам+ит хук",
		"SELECT из JOIN":                      "сил+ект из джойн",
		"закоммичено":                         "закоммич+ено",
		"cursor, coalesce, ORM, e-mail":       "к+урсор, коал+ис, +о ар эм, +и мейл",
		"asset_id union all":                  "эссет ай ди ю+нион +олл",
		"useState ReadAloudPlayer getHTTPResponse obj.method() from x unchanged_id": "юз стейт рид элауд пл+ейер гет эйч ти ти пи риспонс обджект м+етод фром икс анчэйнджд ай ди",
		"count >= 2 && ready != false || done === true":                             "каунт больше или равно два логическое и реади не равно фалсе логическое или доне строго равно труе",
		"x + y, x - y, x * y, x / y":                                                "икс плюс игрек, икс минус игрек, икс умножить на игрек, икс разделить на игрек",
		"2 тысячи":                                                                  "два тысячи",
	}
	for in, want := range cases {
		if got := normalizeRussianText(in); got != want {
			t.Errorf("normalizeRussianText(%q)\n got %q\nwant %q", in, got, want)
		}
	}
}

func TestUnknownLatinWords(t *testing.T) {
	got := unknownLatinWords("deploy через Kubernetesx и API, v2, zorblax zorblax")
	if len(got) != 2 || got[0] != "kubernetesx" || got[1] != "zorblax" {
		t.Fatalf("unexpected unknown words: %v", got)
	}
}

func TestNormalizePaths(t *testing.T) {
	got := normalizeRussianText("Открой /etc/hosts.txt, потом ~/git/app. Дата 1/2/2024")
	for _, want := range []string{"х+остс текстовый файл в и ти си папке", "папка эпп в гит папке внутри домашней"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "слэш") {
		t.Errorf("slashes should not be spoken: %q", got)
	}
	// Dates without letters must not be turned into paths.
	if !strings.Contains(got, "один/два/") {
		t.Errorf("date was rewritten as a path: %q", got)
	}
}

func TestNormalizeDatesAndDivision(t *testing.T) {
	t.Setenv("AAGENT_DATA_PATH", t.TempDir())
	cases := map[string]string{
		"1/2/2024":               "один/два/две тысячи двадцать четыре",
		"01/02/2024, 31/12/2025": "один/два/две тысячи двадцать четыре, тридцать один/двенадцать/две тысячи двадцать пять",
		"6/2":                    "шесть разделить на два",
		"6 / 2":                  "шесть разделить на два",
		"1/2/2024: 6 / 2":        "один/два/две тысячи двадцать четыре: шесть разделить на два",
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			if got := normalizeRussianText(in); got != want {
				t.Errorf("normalizeRussianText(%q) = %q, want %q", in, got, want)
			}
		})
	}
}
