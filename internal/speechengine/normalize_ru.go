package speechengine

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// Silero does not normalize text: digits and Latin words are skipped or swallowed
// (measured: audio is ~2x shorter than expected). normalizeRussianText rewrites
// numbers and Latin words into Cyrillic words before synthesis.

var (
	ruUnits    = []string{"", "один", "два", "три", "четыре", "пять", "шесть", "семь", "восемь", "девять", "десять", "одиннадцать", "двенадцать", "тринадцать", "четырнадцать", "пятнадцать", "шестнадцать", "семнадцать", "восемнадцать", "девятнадцать"}
	ruTens     = []string{"", "", "двадцать", "тридцать", "сорок", "пятьдесят", "шестьдесят", "семьдесят", "восемьдесят", "девяносто"}
	ruHundreds = []string{"", "сто", "двести", "триста", "четыреста", "пятьсот", "шестьсот", "семьсот", "восемьсот", "девятьсот"}

	// Ordinal stems (masculine); the case ending is appended by ordinalWord.
	ruOrdUnits = []string{"", "первый", "второй", "третий", "четвёртый", "пятый", "шестой", "седьмой", "восьмой", "девятый", "десятый", "одиннадцатый", "двенадцатый", "тринадцатый", "четырнадцатый", "пятнадцатый", "шестнадцатый", "семнадцатый", "восемнадцатый", "девятнадцатый"}
	ruOrdTens  = []string{"", "", "двадцатый", "тридцатый", "сороковой", "пятидесятый", "шестидесятый", "семидесятый", "восьмидесятый", "девяностый"}
	ruOrdHund  = []string{"", "сотый", "двухсотый", "трёхсотый", "четырёхсотый", "пятисотый", "шестисотый", "семисотый", "восьмисотый", "девятисотый"}

	reYear     = regexp.MustCompile(`\b(\d{4})(\s+)(год(?:а|у|ом|е)?)([^\p{L}]|$)`)
	rePercent  = regexp.MustCompile(`(\d+)(?:[.,](\d+))?\s*%`)
	reDecimal  = regexp.MustCompile(`\b(\d+)[.,](\d{1,3})\b`)
	reNumber   = regexp.MustCompile(`\d+`)
	reCyrillic = regexp.MustCompile(`[А-Яа-яЁё]+`)
	reEmail    = regexp.MustCompile(`(?i)\be-mail`)
	// rePath matches file paths (absolute, ~/, ./ or relative with a slash); letters are required so dates like 1/2/2024 are left alone.
	rePath     = regexp.MustCompile(`(?:~|\.{1,2})?(?:/[A-Za-z0-9_.\-]+)+/?|\b[A-Za-z0-9_\-]+(?:\.[A-Za-z0-9_\-]+)*(?:/[A-Za-z0-9_.\-]+)+/?`)
	reLetter   = regexp.MustCompile(`[A-Za-z]`)
	reLatin    = regexp.MustCompile(`[A-Za-z][A-Za-z0-9'’]*`)
	reNumGroup = regexp.MustCompile(`\b\d{1,3}(?:[  ]\d{3})+\b`)
)

// fileKinds maps a file extension to how it is said before "файл" (/etc/hosts.txt -> "текстовый файл").
var fileKinds = map[string]string{
	"txt": "текстовый", "md": "маркдаун", "json": "джейсон", "yml": "ямл", "yaml": "ямл", "go": "гоу",
	"ts": "тайпскрипт", "tsx": "тайпскрипт", "js": "джаваскрипт", "jsx": "джаваскрипт", "py": "пайтон",
	"sh": "эс эйч", "sql": "эс кью эл", "html": "эйч ти эм эл", "css": "си эс эс", "toml": "томл",
	"log": "лог", "png": "картинка", "jpg": "картинка", "svg": "эс ви джи", "pdf": "пи ди эф", "db": "база",
}

// pathWords reads a path the way a person would: the last part as a file or folder,
// then its folders from the innermost out ("/etc/hosts.txt" -> "hosts текстовый файл в etc папке").
// Trailing dots are sentence punctuation, not part of the path, and are kept as is.
func pathWords(path string) string {
	if !reLetter.MatchString(path) {
		return path
	}
	trimmed := strings.TrimRight(path, ".")
	tail := path[len(trimmed):]
	var parts []string
	for _, p := range strings.Split(trimmed, "/") {
		switch p {
		case "":
		case ".", "..":
		case "~":
			parts = append(parts, "домашней")
		default:
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return path
	}
	clean := func(s string) string {
		return strings.Join(strings.Fields(strings.NewReplacer("_", " ", "-", " ", ".", " ").Replace(s)), " ")
	}
	last := parts[len(parts)-1]
	dirs := parts[:len(parts)-1]
	var out string
	switch dot := strings.LastIndex(last, "."); {
	case strings.HasSuffix(trimmed, "/") || dot < 0:
		out = "папка " + clean(last)
	case dot <= 0:
		out = "файл точка " + clean(last)
	default:
		ext := strings.ToLower(last[dot+1:])
		if kind, ok := fileKinds[ext]; ok {
			out = clean(last[:dot]) + " " + kind + " файл"
		} else {
			out = clean(last[:dot]) + " файл точка " + ext
		}
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		d := clean(dirs[i])
		if i == len(dirs)-1 {
			out += " в " + d + " папке"
		} else {
			out += " внутри " + d
		}
	}
	return " " + out + " " + tail
}

func normalizeRussianText(text string) string {
	text = rePath.ReplaceAllStringFunc(text, pathWords)
	text = reNumGroup.ReplaceAllStringFunc(text, func(s string) string {
		return strings.NewReplacer(" ", "", " ", "", " ", "").Replace(s)
	})
	text = reYear.ReplaceAllStringFunc(text, func(s string) string {
		m := reYear.FindStringSubmatch(s)
		n, _ := strconv.Atoi(m[1])
		return yearWords(n, m[3]) + m[2] + m[3] + m[4]
	})
	text = rePercent.ReplaceAllStringFunc(text, func(s string) string {
		m := rePercent.FindStringSubmatch(s)
		if m[2] != "" {
			return decimalWords(m[1], m[2]) + " процента"
		}
		n, _ := strconv.ParseInt(m[1], 10, 64)
		return cardinal(n) + " " + pluralForm(n, "процент", "процента", "процентов")
	})
	text = reDecimal.ReplaceAllStringFunc(text, func(s string) string {
		m := reDecimal.FindStringSubmatch(s)
		return decimalWords(m[1], m[2])
	})
	text = reNumber.ReplaceAllStringFunc(text, func(s string) string {
		// Long digit strings (phones, ids) are read digit by digit.
		if len(s) > 9 {
			parts := make([]string, 0, len(s))
			for _, d := range s {
				parts = append(parts, ruUnits[d-'0'])
				if d == '0' {
					parts[len(parts)-1] = "ноль"
				}
			}
			return strings.Join(parts, " ")
		}
		n, _ := strconv.ParseInt(s, 10, 64)
		return cardinal(n)
	})
	// reLatin splits on hyphens, so the dictionary key "e-mail" never matches; handle it first.
	text = reEmail.ReplaceAllString(text, "+и мейл")
	text = reLatin.ReplaceAllStringFunc(text, latinToCyrillic)
	return reCyrillic.ReplaceAllStringFunc(text, func(w string) string {
		if st, ok := lookupPronunciation(w); ok {
			return st
		}
		if st, ok := ruStress[strings.ToLower(w)]; ok {
			return st
		}
		return w
	})
}

func pluralForm(n int64, one, few, many string) string {
	n = n % 100
	if n >= 11 && n <= 19 {
		return many
	}
	switch n % 10 {
	case 1:
		return one
	case 2, 3, 4:
		return few
	}
	return many
}

// cardinal returns masculine nominative words for 0 <= n < 10^12.
func cardinal(n int64) string {
	if n == 0 {
		return "ноль"
	}
	var parts []string
	type scale struct {
		div           int64
		one, few, man string
		feminine      bool
	}
	for _, sc := range []scale{
		{1_000_000_000, "миллиард", "миллиарда", "миллиардов", false},
		{1_000_000, "миллион", "миллиона", "миллионов", false},
		{1_000, "тысяча", "тысячи", "тысяч", true},
	} {
		if chunk := n / sc.div % 1000; chunk > 0 {
			parts = append(parts, triple(chunk, sc.feminine), pluralForm(chunk, sc.one, sc.few, sc.man))
		}
	}
	if rest := n % 1000; rest > 0 {
		parts = append(parts, triple(rest, false))
	}
	return strings.Join(parts, " ")
}

func triple(n int64, feminine bool) string {
	var parts []string
	if h := n / 100; h > 0 {
		parts = append(parts, ruHundreds[h])
	}
	r := n % 100
	if r >= 20 {
		parts = append(parts, ruTens[r/10])
		r %= 10
	}
	if r > 0 {
		w := ruUnits[r]
		if feminine && r == 1 {
			w = "одна"
		} else if feminine && r == 2 {
			w = "две"
		}
		parts = append(parts, w)
	}
	return strings.Join(parts, " ")
}

func decimalWords(intPart, frac string) string {
	n, _ := strconv.ParseInt(intPart, 10, 64)
	f, _ := strconv.ParseInt(frac, 10, 64)
	den := map[int]string{1: "десятых", 2: "сотых", 3: "тысячных"}[len(frac)]
	return feminine(cardinal(n)) + " " + pluralForm(n, "целая", "целых", "целых") + " " + feminine(cardinal(f)) + " " + den
}

// feminine turns a trailing "один"/"два" into "одна"/"две" (e.g. "одна целая", "две десятых").
func feminine(words string) string {
	switch {
	case strings.HasSuffix(words, "один"):
		return strings.TrimSuffix(words, "один") + "одна"
	case strings.HasSuffix(words, "два"):
		return strings.TrimSuffix(words, "два") + "две"
	}
	return words
}

// yearWords reads "2024 году" as "две тысячи двадцать четвёртом".
func yearWords(n int, yearWord string) string {
	rem := n % 1000
	if rem == 0 {
		return cardinal(int64(n))
	}
	ending := map[string]string{"год": "ый", "года": "ого", "году": "ом", "годом": "ым", "годе": "ом"}[yearWord]
	var parts []string
	if th := n / 1000; th > 0 {
		parts = append(parts, triple(int64(th), true), pluralForm(int64(th), "тысяча", "тысячи", "тысяч"))
	}
	if h := rem / 100; h > 0 && rem%100 == 0 {
		parts = append(parts, ordinalEnding(ruOrdHund[h], ending))
		return strings.Join(parts, " ")
	} else if h > 0 {
		parts = append(parts, ruHundreds[h])
	}
	r := rem % 100
	switch {
	case r >= 20 && r%10 == 0:
		parts = append(parts, ordinalEnding(ruOrdTens[r/10], ending))
	case r >= 20:
		parts = append(parts, ruTens[r/10], ordinalEnding(ruOrdUnits[r%10], ending))
	default:
		parts = append(parts, ordinalEnding(ruOrdUnits[r], ending))
	}
	return strings.Join(parts, " ")
}

// ordinalEnding swaps the nominative masculine ending (-ый/-ой/-ий) for the requested one.
func ordinalEnding(word, ending string) string {
	for _, suf := range []string{"ый", "ой", "ий"} {
		if strings.HasSuffix(word, suf) {
			stem := strings.TrimSuffix(word, suf)
			// After soft/sibilant stems and "третий" the endings differ.
			if suf == "ий" {
				return stem + map[string]string{"ый": "ий", "ого": "ьего", "ому": "ьему", "ым": "ьим", "ом": "ьем"}[ending]
			}
			if suf == "ой" && ending != "ый" {
				// -ой stems are stressed on the ending: ending keeps "о" forms with ь-less stem.
				return stem + map[string]string{"ого": "ого", "ому": "ому", "ым": "ым", "ом": "ом"}[ending]
			}
			return stem + ending
		}
	}
	return word
}

var latinLetters = map[rune]string{
	'a': "эй", 'b': "би", 'c': "си", 'd': "ди", 'e': "и", 'f': "эф", 'g': "джи", 'h': "эйч", 'i': "ай",
	'j': "джей", 'k': "кей", 'l': "эль", 'm': "эм", 'n': "эн", 'o': "оу", 'p': "пи", 'q': "кью", 'r': "ар",
	's': "эс", 't': "ти", 'u': "ю", 'v': "ви", 'w': "дабл ю", 'x': "икс", 'y': "уай", 'z': "зед",
}

// Rough letter-level transliteration for words missing from latinDict.
var latinDigraphs = []struct{ from, to string }{
	{"tch", "ч"}, {"sch", "ск"}, {"ch", "ч"}, {"sh", "ш"}, {"th", "т"}, {"ph", "ф"}, {"ck", "к"},
	{"ee", "и"}, {"oo", "у"}, {"ou", "ау"}, {"qu", "кв"}, {"kh", "х"}, {"zh", "ж"}, {"ts", "ц"},
}

var latinSingles = map[rune]string{
	'a': "а", 'b': "б", 'c': "к", 'd': "д", 'e': "е", 'f': "ф", 'g': "г", 'h': "х", 'i': "и", 'j': "дж",
	'k': "к", 'l': "л", 'm': "м", 'n': "н", 'o': "о", 'p': "п", 'q': "к", 'r': "р", 's': "с", 't': "т",
	'u': "у", 'v': "в", 'w': "в", 'x': "кс", 'y': "и", 'z': "з",
}

func latinToCyrillic(word string) string {
	word = strings.ReplaceAll(word, "’", "'")
	lower := strings.ToLower(word)
	if w, ok := lookupPronunciation(lower); ok {
		return w
	}
	if w, ok := latinDict[lower]; ok {
		return w
	}
	if w, ok := latinInflected(lower); ok {
		return w
	}
	if i := strings.IndexAny(lower, "0123456789"); i >= 0 {
		// Mixed tokens like "v2" or "h264": read the letters, digits get handled by reNumber earlier.
		return spellLatin(lower)
	}
	letters := []rune(word)
	upper := 0
	for _, r := range letters {
		if unicode.IsUpper(r) {
			upper++
		}
	}
	// ALL-CAPS short tokens are acronyms: spell them out.
	if len(letters) >= 2 && len(letters) <= 5 && upper == len(letters) {
		return spellLatin(lower)
	}
	for _, d := range latinDigraphs {
		lower = strings.ReplaceAll(lower, d.from, "\x00"+d.to+"\x00")
	}
	var b strings.Builder
	for _, seg := range strings.Split(lower, "\x00") {
		if seg == "" {
			continue
		}
		if r := []rune(seg); r[0] >= 'а' && r[0] <= 'я' {
			b.WriteString(seg)
			continue
		}
		for _, r := range seg {
			b.WriteString(latinSingles[r])
		}
	}
	return b.String()
}

func spellLatin(s string) string {
	var parts []string
	for _, r := range s {
		if l, ok := latinLetters[r]; ok {
			parts = append(parts, l)
		}
	}
	return strings.Join(parts, " ")
}

// latinInflected reads dictionary words with English endings (commits, pushed, merging)
// by gluing a Russian-sounding suffix to the known stem.
func latinInflected(lower string) (string, bool) {
	for _, suf := range []struct{ en, ru string }{{"ing", "инг"}, {"ed", "ед"}, {"es", "ы"}, {"s", "ы"}} {
		stem, ok := strings.CutSuffix(lower, suf.en)
		if !ok {
			continue
		}
		// Also try the stem with a dropped "e" restored: merging -> merge.
		for _, cand := range []string{stem, stem + "e"} {
			if base, found := latinDict[cand]; found && !strings.HasSuffix(base, " ") {
				if suf.en == "s" || suf.en == "es" {
					if strings.HasSuffix(base, "г") || strings.HasSuffix(base, "к") || strings.HasSuffix(base, "х") {
						return base + "и", true
					}
					return base + "ы", true
				}
				return base + suf.ru, true
			}
		}
	}
	return "", false
}

// ruStress fixes Russian words Silero stresses wrongly (mostly dev jargon).
// "+" precedes the stressed vowel.
var ruStress = map[string]string{
	"закоммичено": "закоммич+ено", "закоммичен": "закоммич+ен", "закоммичены": "закоммич+ены",
	"закоммичу": "закомм+ичу", "закоммитил": "закомм+итил", "закоммитить": "закомм+итить",
	"закоммитили": "закомм+итили", "закоммитила": "закомм+итила",
}
