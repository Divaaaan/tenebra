// Best-effort geographic inference from a node's display name. Tenebra is a
// generic client to user-supplied subscriptions, so the node objects carry no
// country/city field — only a free-form name (e.g. "🇩🇪 DE-FRA-01",
// "Germany-Frankfurt #3", "us-nyc"). The server-list region chips and the row
// subtitle need a region and a human label, so we derive them from that name.
//
// This is explicitly heuristic: anything we can't place lands in "OTHER" and is
// shown without a subtitle rather than guessed wrong. Detection runs strongest
// signal first — a flag emoji, then an airport code, then an ISO country code,
// then city/country keywords.

export type Region = "EU" | "AM" | "AP" | "OTHER";

export interface NodeLocation {
  region: Region;
  /** ISO 3166-1 alpha-2, when we could place the node. */
  country?: string;
  /** Lowercased human subtitle for the row (city or country); "" if unknown. */
  label: string;
}

// ISO country → region + display name. Covers the locations that actually show
// up in real subscriptions (EU-heavy, plus the Americas and Asia-Pacific the
// chips name). Unknown countries fall through to OTHER.
const COUNTRIES: Record<string, { region: Region; name: string }> = {
  NL: { region: "EU", name: "netherlands" },
  DE: { region: "EU", name: "germany" },
  SE: { region: "EU", name: "sweden" },
  GB: { region: "EU", name: "united kingdom" },
  CH: { region: "EU", name: "switzerland" },
  FR: { region: "EU", name: "france" },
  FI: { region: "EU", name: "finland" },
  NO: { region: "EU", name: "norway" },
  DK: { region: "EU", name: "denmark" },
  PL: { region: "EU", name: "poland" },
  IT: { region: "EU", name: "italy" },
  ES: { region: "EU", name: "spain" },
  AT: { region: "EU", name: "austria" },
  IE: { region: "EU", name: "ireland" },
  CZ: { region: "EU", name: "czechia" },
  RO: { region: "EU", name: "romania" },
  LV: { region: "EU", name: "latvia" },
  LT: { region: "EU", name: "lithuania" },
  EE: { region: "EU", name: "estonia" },
  RU: { region: "EU", name: "russia" },
  UA: { region: "EU", name: "ukraine" },
  TR: { region: "EU", name: "turkey" },
  BG: { region: "EU", name: "bulgaria" },
  RS: { region: "EU", name: "serbia" },
  PT: { region: "EU", name: "portugal" },
  GR: { region: "EU", name: "greece" },
  BE: { region: "EU", name: "belgium" },
  IS: { region: "EU", name: "iceland" },
  MD: { region: "EU", name: "moldova" },
  US: { region: "AM", name: "united states" },
  CA: { region: "AM", name: "canada" },
  BR: { region: "AM", name: "brazil" },
  MX: { region: "AM", name: "mexico" },
  AR: { region: "AM", name: "argentina" },
  CL: { region: "AM", name: "chile" },
  CO: { region: "AM", name: "colombia" },
  JP: { region: "AP", name: "japan" },
  SG: { region: "AP", name: "singapore" },
  HK: { region: "AP", name: "hong kong" },
  AU: { region: "AP", name: "australia" },
  NZ: { region: "AP", name: "new zealand" },
  KR: { region: "AP", name: "south korea" },
  IN: { region: "AP", name: "india" },
  ID: { region: "AP", name: "indonesia" },
  MY: { region: "AP", name: "malaysia" },
  TH: { region: "AP", name: "thailand" },
  VN: { region: "AP", name: "vietnam" },
  TW: { region: "AP", name: "taiwan" },
  PH: { region: "AP", name: "philippines" },
  KZ: { region: "AP", name: "kazakhstan" },
};

// Airport / IATA-ish codes → [country, city]. These also give us a city label,
// which the bare ISO code can't.
const AIRPORTS: Record<string, [string, string]> = {
  AMS: ["NL", "amsterdam"],
  FRA: ["DE", "frankfurt"],
  BER: ["DE", "berlin"],
  DUS: ["DE", "düsseldorf"],
  LON: ["GB", "london"],
  LHR: ["GB", "london"],
  MAN: ["GB", "manchester"],
  STO: ["SE", "stockholm"],
  ARN: ["SE", "stockholm"],
  ZRH: ["CH", "zürich"],
  GVA: ["CH", "geneva"],
  PAR: ["FR", "paris"],
  CDG: ["FR", "paris"],
  MRS: ["FR", "marseille"],
  HEL: ["FI", "helsinki"],
  OSL: ["NO", "oslo"],
  CPH: ["DK", "copenhagen"],
  WAW: ["PL", "warsaw"],
  MIL: ["IT", "milan"],
  MXP: ["IT", "milan"],
  MAD: ["ES", "madrid"],
  VIE: ["AT", "vienna"],
  DUB: ["IE", "dublin"],
  PRG: ["CZ", "prague"],
  BUH: ["RO", "bucharest"],
  RIX: ["LV", "riga"],
  TLL: ["EE", "tallinn"],
  MOW: ["RU", "moscow"],
  LED: ["RU", "saint petersburg"],
  KBP: ["UA", "kyiv"],
  IST: ["TR", "istanbul"],
  NYC: ["US", "new york"],
  JFK: ["US", "new york"],
  EWR: ["US", "new york"],
  LAX: ["US", "los angeles"],
  MIA: ["US", "miami"],
  ORD: ["US", "chicago"],
  SEA: ["US", "seattle"],
  DFW: ["US", "dallas"],
  ATL: ["US", "atlanta"],
  SFO: ["US", "san francisco"],
  TOR: ["CA", "toronto"],
  YYZ: ["CA", "toronto"],
  YUL: ["CA", "montreal"],
  GRU: ["BR", "são paulo"],
  SAO: ["BR", "são paulo"],
  TYO: ["JP", "tokyo"],
  NRT: ["JP", "tokyo"],
  HND: ["JP", "tokyo"],
  OSA: ["JP", "osaka"],
  SIN: ["SG", "singapore"],
  HKG: ["HK", "hong kong"],
  SYD: ["AU", "sydney"],
  MEL: ["AU", "melbourne"],
  ICN: ["KR", "seoul"],
  SEL: ["KR", "seoul"],
  BOM: ["IN", "mumbai"],
  DEL: ["IN", "delhi"],
  BRU: ["BE", "brussels"],
};

// ISO codes that are also common English words, so a bare two-letter token is
// not enough evidence — these need a flag, an airport code, or a spelled-out
// name. Prevents "my-private-relay" → Malaysia, "in-test" → India, etc.
const AMBIGUOUS = new Set(["MY", "IN", "IT", "IS", "AT", "BE", "NO", "ID"]);

// City / country keyword → ISO country. Substring match (case-insensitive),
// English plus the Russian forms that turn up in real-world remarks.
const KEYWORDS: Record<string, string> = {
  amsterdam: "NL",
  netherlands: "NL",
  нидерланды: "NL",
  голландия: "NL",
  frankfurt: "DE",
  berlin: "DE",
  germany: "DE",
  германия: "DE",
  stockholm: "SE",
  sweden: "SE",
  швеция: "SE",
  london: "GB",
  britain: "GB",
  england: "GB",
  британия: "GB",
  англия: "GB",
  zurich: "CH",
  zürich: "CH",
  switzerland: "CH",
  швейцария: "CH",
  paris: "FR",
  france: "FR",
  франция: "FR",
  helsinki: "FI",
  finland: "FI",
  финляндия: "FI",
  oslo: "NO",
  norway: "NO",
  warsaw: "PL",
  poland: "PL",
  польша: "PL",
  milan: "IT",
  italy: "IT",
  италия: "IT",
  madrid: "ES",
  spain: "ES",
  испания: "ES",
  vienna: "AT",
  austria: "AT",
  dublin: "IE",
  ireland: "IE",
  prague: "CZ",
  moscow: "RU",
  russia: "RU",
  россия: "RU",
  москва: "RU",
  istanbul: "TR",
  turkey: "TR",
  турция: "TR",
  "new york": "US",
  "los angeles": "US",
  miami: "US",
  chicago: "US",
  dallas: "US",
  seattle: "US",
  america: "US",
  "united states": "US",
  usa: "US",
  сша: "US",
  америка: "US",
  toronto: "CA",
  montreal: "CA",
  canada: "CA",
  канада: "CA",
  "são paulo": "BR",
  "sao paulo": "BR",
  brazil: "BR",
  бразилия: "BR",
  tokyo: "JP",
  osaka: "JP",
  japan: "JP",
  япония: "JP",
  singapore: "SG",
  сингапур: "SG",
  "hong kong": "HK",
  sydney: "AU",
  australia: "AU",
  австралия: "AU",
  seoul: "KR",
  korea: "KR",
  mumbai: "IN",
  india: "IN",
  dubai: "AE",
};

function place(country: string, label: string): NodeLocation {
  const entry = COUNTRIES[country];
  return {
    region: entry?.region ?? "OTHER",
    country,
    label: label || entry?.name || "",
  };
}

/** Decode the first flag emoji (a pair of regional-indicator symbols) to ISO2. */
function flagCountry(name: string): string | undefined {
  const points = Array.from(name);
  for (let i = 0; i < points.length - 1; i++) {
    const a = points[i].codePointAt(0) ?? 0;
    const b = points[i + 1].codePointAt(0) ?? 0;
    if (a >= 0x1f1e6 && a <= 0x1f1ff && b >= 0x1f1e6 && b <= 0x1f1ff) {
      return (
        String.fromCharCode(a - 0x1f1e6 + 65) +
        String.fromCharCode(b - 0x1f1e6 + 65)
      );
    }
  }
  return undefined;
}

/**
 * Infer a node's region and a human label from its display name. Strongest
 * signal wins; unrecognised names return { region: "OTHER", label: "" }.
 */
export function locate(name: string): NodeLocation {
  const flag = flagCountry(name);
  if (flag && COUNTRIES[flag]) {
    return place(flag, "");
  }

  // Tokenise on non-alphanumerics so "NL-AMS-03" yields NL, AMS, 03.
  const tokens = name.toUpperCase().split(/[^A-Z0-9]+/).filter(Boolean);
  for (const tok of tokens) {
    const air = AIRPORTS[tok];
    if (air) {
      return place(air[0], air[1]);
    }
  }
  for (const tok of tokens) {
    if (tok.length === 2 && COUNTRIES[tok] && !AMBIGUOUS.has(tok)) {
      return place(tok, "");
    }
  }

  const lower = name.toLowerCase();
  for (const [kw, country] of Object.entries(KEYWORDS)) {
    if (lower.includes(kw)) {
      // A multi-word keyword is itself a fine city label; a country keyword
      // falls back to the country name via place().
      return place(country, kw.includes(" ") ? kw : "");
    }
  }

  return { region: "OTHER", label: "" };
}

/** Region chip definitions, in display order. `key` is null for "all". */
export const REGION_CHIPS: { key: Region | null; labelKey: string }[] = [
  { key: null, labelKey: "all" },
  { key: "EU", labelKey: "europe" },
  { key: "AM", labelKey: "americas" },
  { key: "AP", labelKey: "asiaPac" },
];
