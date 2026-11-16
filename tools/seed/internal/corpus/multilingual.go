package corpus

// Snippet is a sentence in another language, with an English gloss kept for
// the documentation and for agent evals that ask about it.
type Snippet struct {
	Language string // BCP 47 tag
	Text     string
	Gloss    string
}

// Multilingual are short engineering-flavoured sentences in Hindi, Japanese
// and German. Notes generated "in" a language use several of them; others
// quote one. Hindi and Japanese exercise non-Latin scripts (and Japanese has
// no spaces between words); German exercises umlauts and long compounds.
var Multilingual = []Snippet{
	{"hi", "रेप्लिका ऑफ़लाइन होने पर भी बदलाव सुरक्षित रहते हैं।", "Changes stay safe even when a replica is offline."},
	{"hi", "लीडर चुनाव का टाइमआउट डेढ़ सौ से तीन सौ मिलीसेकंड के बीच रखें।", "Keep the leader election timeout between 150 and 300 ms."},
	{"hi", "हर बदलाव के बाद इनवेरिएंट की जाँच करें।", "Check the invariants after every change."},
	{"hi", "इस हफ़्ते की बैठक में खोज की गति पर बात हुई।", "This week's meeting discussed search speed."},
	{"hi", "डेटाबेस माइग्रेशन रात में चलाना सुरक्षित है।", "Running the database migration at night is safe."},
	{"hi", "कैश की समय-सीमा में थोड़ा यादृच्छिक अंतर जोड़ें।", "Add a little random jitter to cache expiry."},
	{"ja", "オフラインでも編集は失われません。", "Edits are not lost even offline."},
	{"ja", "リーダー選挙のタイムアウトは百五十から三百ミリ秒です。", "The leader election timeout is 150 to 300 ms."},
	{"ja", "コンパクションが遅れると書き込みが止まります。", "When compaction falls behind, writes stall."},
	{"ja", "検索結果は権限で絞り込んでから返します。", "Search results are filtered by permission before returning."},
	{"ja", "次回の振り返りは金曜日の午後です。", "The next retrospective is Friday afternoon."},
	{"ja", "ベクトルは正規化してから索引に入れます。", "Vectors are normalised before going into the index."},
	{"de", "Die Replikationsverzögerung stieg während der Wartung auf über zwei Minuten.", "Replication lag rose above two minutes during maintenance."},
	{"de", "Bitte prüft die Zugriffsrechte, bevor ihr den Freigabelink verschickt.", "Please check the access rights before sending the share link."},
	{"de", "Die Speicherverwaltung der Kompaktierung ist überraschend schwierig.", "Memory management in compaction is surprisingly hard."},
	{"de", "Übermorgen führen wir die Datenbankmigration durch.", "The day after tomorrow we run the database migration."},
	{"de", "Größere Batches verbessern den Durchsatz, aber nicht die Latenz.", "Bigger batches improve throughput but not latency."},
	{"de", "Für Rückfragen bin ich bis Donnerstag erreichbar.", "I am reachable for questions until Thursday."},
}

// Syllables build pronounceable invented words (codenames). Codenames are
// planted into a few notes per workspace and give text search queries whose
// relevant set is small and exactly known.
var Syllables = []string{
	"quil", "fen", "mar", "row", "tin", "dle", "shore", "brack", "wyn", "gal", "lor", "vex", "tor", "pim",
	"zan", "dor", "kel", "mire", "sorn", "thal", "ulk", "vor", "yen", "bray", "cask", "drel", "fro", "gim",
}
