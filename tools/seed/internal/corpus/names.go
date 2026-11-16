// Package corpus holds the raw material the seed generator writes notes
// from: invented people and companies, topic models (vocabularies, phrase
// and title templates, code templates), multilingual snippets, and the
// hand-written anchor workspace.
//
// Everything here is fictional. Person names are random pairings of common
// given names and family names from many cultures; companies and their
// domains are invented and use the reserved ".example" top-level domain
// (RFC 2606), so no generated address can reach a real mailbox.
//
// Lists are ordered slices, never maps, so generation never depends on map
// iteration order.
package corpus

// GivenNames are combined with FamilyNames to make invented people.
var GivenNames = []string{
	"Aarav", "Abena", "Adaeze", "Aiko", "Alejandro", "Amara", "Anika", "Arjun", "Astrid", "Beatriz",
	"Bilal", "Bodhi", "Camila", "Chen", "Chiara", "Dagny", "Dario", "Deepa", "Elif", "Emeka",
	"Esme", "Farah", "Felix", "Freya", "Gideon", "Hana", "Hiro", "Ibrahim", "Ines", "Isla",
	"Jonas", "Juniper", "Kaito", "Kavya", "Keanu", "Kenji", "Lars", "Leila", "Lucia", "Mads",
	"Malik", "Mateo", "Meera", "Mika", "Nadia", "Nikolai", "Noor", "Oskar", "Priya", "Quentin",
	"Rafael", "Rania", "Rohan", "Saanvi", "Sakura", "Selin", "Soren", "Tariq", "Thandiwe", "Theo",
	"Uma", "Valeria", "Vikram", "Wanjiru", "Xavier", "Yara", "Yusuf", "Zainab", "Zoltan", "Ines",
	"Otso", "Paloma", "Rin", "Sven", "Tomasz", "Ulla", "Wen", "Yosef", "Ana", "Bo",
}

// FamilyNames are combined with GivenNames to make invented people. A few
// include non-ASCII letters so display names exercise Unicode handling.
var FamilyNames = []string{
	"Abernethy", "Achebe", "Adeyemi", "Alvarez", "Andersen", "Bakshi", "Balogun", "Bergström", "Castellano", "Chaudhry",
	"Dąbrowski", "Delacroix", "Eriksen", "Fairweather", "Fujimoto", "Galloway", "Gonçalves", "Halvorsen", "Haddad", "Ishikawa",
	"Iyer", "Jankowski", "Kapoor", "Kowalczyk", "Lindqvist", "Lombardi", "Mbeki", "Moreau", "Nakamura", "Nwosu",
	"Okonkwo", "Oyelaran", "Pellegrino", "Petrov", "Quaresma", "Rahman", "Reyes", "Sato", "Schäfer", "Sorensen",
	"Takahashi", "Tanaka", "Underhill", "Varga", "Venkataraman", "Wainwright", "Watanabe", "Xu", "Yilmaz", "Zielinski",
	"Ashby", "Brannigan", "Cortázar", "Drummond", "Ekström", "Fontaine", "Grünewald", "Hollis", "Ikeda", "Jovanović",
	"Kerrigan", "Larsen", "Mukherjee", "Novak", "Ortega", "Prakash", "Quill", "Rasmussen", "Strand", "Thorne",
}

// Company is an invented organisation that owns a workspace.
type Company struct {
	Name   string
	Domain string
}

// Companies are invented organisations. Domains use ".example".
var Companies = []Company{
	{"Tallowmere Logistics", "tallowmere.example"},
	{"Brindlecove Health", "brindlecove.example"},
	{"Quarrystone Analytics", "quarrystone.example"},
	{"Fernhollow Games", "fernhollow.example"},
	{"Cobaltreach Energy", "cobaltreach.example"},
	{"Marrowfield Insurance", "marrowfield.example"},
	{"Pinecrest Robotics", "pinecrest-robotics.example"},
	{"Saltmarsh Media", "saltmarsh.example"},
	{"Thistlewick Foods", "thistlewick.example"},
	{"Umberline Payments", "umberline.example"},
	{"Velvetrun Travel", "velvetrun.example"},
	{"Wrenfold Education", "wrenfold.example"},
	{"Ashgrove Biotech", "ashgrove-bio.example"},
	{"Bellhaven Retail", "bellhaven.example"},
	{"Copperleaf Studios", "copperleaf.example"},
	{"Driftmoor Shipping", "driftmoor.example"},
	{"Emberfall Security", "emberfall.example"},
	{"Foxglove Telecom", "foxglove-tel.example"},
	{"Glimmerholt Labs", "glimmerholt.example"},
	{"Hartwell Mapping", "hartwell-maps.example"},
	{"Ironbark Construction", "ironbark.example"},
	{"Juniperhill Credit", "juniperhill.example"},
	{"Kestrelmoor Aviation", "kestrelmoor.example"},
	{"Larchmont Pharmacy", "larchmont-rx.example"},
	{"Mistral Weatherworks", "mistral-wx.example"},
	{"Nettlebrook Farms", "nettlebrook.example"},
	{"Oakhaven Legal", "oakhaven-legal.example"},
	{"Primrose Ticketing", "primrose-tix.example"},
	{"Quillon Publishing", "quillon.example"},
	{"Rookwood Mobility", "rookwood.example"},
	{"Sablecrest Hotels", "sablecrest.example"},
	{"Tidewater Utilities", "tidewater-util.example"},
	{"Underbridge Archives", "underbridge.example"},
	{"Vantablue Optics", "vantablue.example"},
	{"Willowmere Clinics", "willowmere.example"},
	{"Yarrowgate Capital", "yarrowgate.example"},
	{"Zephyrline Rail", "zephyrline.example"},
	{"Amberwick Software", "amberwick.example"},
	{"Blackthorn Mining", "blackthorn.example"},
	{"Cinderpath Fitness", "cinderpath.example"},
	{"Duskwater Breweries", "duskwater.example"},
	{"Elmstead Housing", "elmstead.example"},
	{"Frostvale Cold Chain", "frostvale.example"},
	{"Gorsefield Telemetry", "gorsefield.example"},
	{"Heathergate Charity", "heathergate.example"},
	{"Inkwell Translation", "inkwell-tx.example"},
	{"Jadecliff Semiconductors", "jadecliff.example"},
	{"Kindlewood Childcare", "kindlewood.example"},
	{"Lanternfish Marine", "lanternfish.example"},
	{"Moonrake Observatory", "moonrake.example"},
	{"Northfold Archive Services", "northfold.example"},
}

// AnchorCompany owns the hand-written anchor workspace.
var AnchorCompany = Company{"Kestrel Lab", "kestrel-lab.example"}

// CastMember is one fixed person in the anchor workspace. Agent evals and
// permission cases refer to them by Key.
type CastMember struct {
	Key         string
	DisplayName string
	Handle      string
	Role        string // workspace role in the anchor workspace
	// RemovedDays, when positive, removes the member this many days before
	// the reference time.
	RemovedDays int
	Locale      string
	TimeZone    string
}

// AnchorCast are the anchor workspace's members, in creation order. The
// first is the owner.
var AnchorCast = []CastMember{
	{Key: "owner", DisplayName: "Mira Okafor-Lindqvist", Handle: "mira", Role: "owner", Locale: "en-GB", TimeZone: "Europe/Stockholm"},
	{Key: "admin", DisplayName: "Tobias Hartmann-Iyer", Handle: "tobias", Role: "admin", Locale: "de-DE", TimeZone: "Europe/Berlin"},
	{Key: "sre", DisplayName: "Priyanka Venkatesh-Moreau", Handle: "priyanka", Role: "member", Locale: "en-IN", TimeZone: "Asia/Kolkata"},
	{Key: "storage", DisplayName: "Kenta Ishikawa-Reyes", Handle: "kenta", Role: "member", Locale: "ja-JP", TimeZone: "Asia/Tokyo"},
	{Key: "ml", DisplayName: "Ottilie Fairweather", Handle: "ottilie", Role: "member", Locale: "en-US", TimeZone: "America/Chicago"},
	{Key: "viewer", DisplayName: "Rasmus Underhill", Handle: "rasmus", Role: "member", Locale: "da-DK", TimeZone: "Europe/Copenhagen"},
	{Key: "guest", DisplayName: "Ifeoma Drummond-Sato", Handle: "ifeoma", Role: "guest", Locale: "en-NG", TimeZone: "Africa/Lagos"},
	{Key: "leaver", DisplayName: "Casimir Quaresma", Handle: "casimir", Role: "member", RemovedDays: 12, Locale: "pt-PT", TimeZone: "Europe/Lisbon"},
}

// Locales and time zones given to generated users, paired by index.
var Locales = []struct{ Locale, TimeZone string }{
	{"en-US", "America/New_York"}, {"en-GB", "Europe/London"}, {"de-DE", "Europe/Berlin"},
	{"ja-JP", "Asia/Tokyo"}, {"hi-IN", "Asia/Kolkata"}, {"pt-BR", "America/Sao_Paulo"},
	{"fr-FR", "Europe/Paris"}, {"es-MX", "America/Mexico_City"}, {"sv-SE", "Europe/Stockholm"},
	{"en-AU", "Australia/Sydney"}, {"yo-NG", "Africa/Lagos"}, {"tr-TR", "Europe/Istanbul"},
}

// Emoji sprinkled into some notes; several are multi-code-point sequences
// (ZWJ families, flags, skin tones) so text handling is tested on clusters
// the engine counts as several code points.
var Emoji = []string{
	"🚀", "✅", "⚠️", "🔥", "🧪", "📌", "🐛", "💡", "👍🏽", "🎉", "🇯🇵", "👩‍💻", "👨‍👩‍👧‍👦", "🧵", "📈", "🤔",
}
