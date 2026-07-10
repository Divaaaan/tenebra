// Bilingual UI strings. The two dictionaries share one shape so a missing key is
// a compile error rather than a silent fallback. Interpolation is done by the
// caller; values here are plain strings.

export type Language = "en" | "ru";

export interface Strings {
  appName: string;
  /** Short brand tagline shown under the wordmark in the sidebar. */
  tagline: string;

  nav: {
    home: string;
    profiles: string;
    settings: string;
    logs: string;
  };

  state: {
    idle: string;
    connecting: string;
    connected: string;
    error: string;
  };

  /** Top bar: brand line + the active subscription's meta. */
  topbar: {
    noSubscription: string;
  };

  /** Left connection pane. */
  conn: {
    eyebrow: string;
    subOff: string;
    subPending: string;
    /** Trailing reassurance on the connected sub-line; prefix is server·proto. */
    subConnected: string;
    abort: string;
    exitIp: string;
    change: string;
    statSession: string;
    statDown: string;
    statPing: string;
    unitMinSec: string;
    unitMbps: string;
  };

  /** Right server-list pane. */
  servers: {
    title: string;
    online: string;
    showing: string;
    regionAll: string;
    regionEurope: string;
    regionAmericas: string;
    regionAsiaPac: string;
    searchPlaceholder: string;
    emptyFilter: string;
    down: string;
    addSub: string;
    noNodes: string;
    /** Compact badge on a node whose TLS certificate is not verified. */
    insecureBadge: string;
    /** Full tooltip / screen-reader label for the insecure badge. */
    insecureTitle: string;
    /** Per-profile summary; "{n}"/"{m}" interpolated (insecure / total nodes). */
    insecureSummary: string;
  };

  /** Bottom bar: routing segmented control, kill-switch, quick actions. */
  bottom: {
    routing: string;
    killSwitch: string;
    killBanner: string;
    /** Tooltip explaining what arming actually does (and costs). */
    killSwitchHint: string;
    leakCheck: string;
    settings: string;
  };

  /** Update banner shown when the launch check finds a newer release. */
  update: {
    /** "{version}" interpolated by the caller. */
    available: string;
    install: string;
    later: string;
    downloading: string;
  };

  home: {
    connect: string;
    disconnect: string;
    cancel: string;
    quickConnect: string;
    noProfile: string;
    noProfileHint: string;
    upload: string;
    download: string;
    activeProfile: string;
    activeNode: string;
    autoNode: string;
    sessionTraffic: string;
  };

  profiles: {
    title: string;
    empty: string;
    emptyHint: string;
    nodes: string;
    active: string;
    setActive: string;
    refresh: string;
    remove: string;
    removeConfirm: string;
    pingAll: string;
    pinging: string;
    autoSelect: string;
    expires: string;
    /** Relative expiry, "{days}" interpolated by the caller. */
    expiresIn: string;
    expiresToday: string;
    expiresTomorrow: string;
    expired: string;
    traffic: string;
    updated: string;
    source: {
      subscription: string;
      manual: string;
    };
    import: {
      title: string;
      tabSubscription: string;
      tabLink: string;
      tabFile: string;
      tabQr: string;
      name: string;
      namePlaceholder: string;
      url: string;
      urlPlaceholder: string;
      link: string;
      linkPlaceholder: string;
      /** Hint under the link field noting several links (one per line) are OK. */
      linkHint: string;
      pickFile: string;
      fileHint: string;
      paste: string;
      qrPick: string;
      qrPasteImage: string;
      qrHint: string;
      qrScanning: string;
      submit: string;
      importing: string;
      /**
       * Outcome of a batch import, with `{imported}` and `{skipped}` counts
       * substituted in.
       */
      batchResult: string;
    };
  };

  settings: {
    title: string;
    routing: string;
    routingSmart: string;
    routingSmartHint: string;
    routingGlobal: string;
    routingGlobalHint: string;
    routingDirect: string;
    routingDirectHint: string;
    split: string;
    splitHint: string;
    splitOff: string;
    splitOffHint: string;
    splitExclude: string;
    splitExcludeHint: string;
    splitInclude: string;
    splitIncludeHint: string;
    splitApps: string;
    splitAppsEmpty: string;
    splitAddPlaceholder: string;
    splitAdd: string;
    splitRemove: string;
    tunnel: string;
    tunnelHint: string;
    stackSystem: string;
    stackSystemHint: string;
    stackGvisor: string;
    stackGvisorHint: string;
    stackMixed: string;
    stackMixedHint: string;
    appearance: string;
    theme: string;
    themeDark: string;
    themeLight: string;
    language: string;
    startup: string;
    launchAtLogin: string;
    launchAtLoginHint: string;
    autoconnect: string;
    autoconnectHint: string;
    /** "Auto-select fastest node" toggle label and its explanation. */
    autoFastest: string;
    autoFastestHint: string;
    /** Updates section: check button plus the status line beside it. */
    updates: string;
    updatesCheck: string;
    updatesChecking: string;
    updatesUpToDate: string;
    /** "{version}" interpolated by the caller. */
    updatesAvailable: string;
    updatesDownloading: string;
    updatesInstall: string;
    updatesInstalling: string;
    updatesError: string;
    /** "Current version {version}", shown while idle. */
    updatesCurrent: string;
    /** "Install updates automatically" toggle label and its explanation. */
    autoInstall: string;
    autoInstallHint: string;
    /** DNS section: the ad/tracker-block toggle and the two custom resolvers. */
    dns: string;
    dnsHint: string;
    adBlock: string;
    adBlockHint: string;
    dnsRemote: string;
    dnsRemoteHint: string;
    dnsDirect: string;
    dnsDirectHint: string;
    /** Example shown in an emptied resolver field. */
    dnsPlaceholder: string;
    /** Localized hint shown when a typed resolver is malformed. */
    dnsInvalid: string;
    /** Update-channel radiogroup: label plus the stable/beta options. */
    updateChannel: string;
    updateChannelStable: string;
    updateChannelStableHint: string;
    updateChannelBeta: string;
    updateChannelBetaHint: string;
  };

  logs: {
    title: string;
    empty: string;
    clear: string;
    leakCheck: string;
    checking: string;
    /** Section heading for the public-IP finding. */
    leakIpHeading: string;
    /** Section heading for the DNS finding. */
    leakDnsHeading: string;
    /** Headlines keyed to the core's ip_verdict. */
    leakIpOk: string;
    leakIpWarn: string;
    leakIpNeutral: string;
    leakIpError: string;
    /** Label for the observed public IP. */
    egressIp: string;
    /** Label for the tunnel exit address we compared against. */
    leakExitServer: string;
    /** Trailing "via {source}" attribution for the echo endpoint. */
    leakSource: string;
    /** DNS status labels, keyed to the core's dns.status. */
    leakDnsOk: string;
    leakDnsLeak: string;
    leakDnsInconclusive: string;
    leakDnsUnavailable: string;
    /** Label preceding the observed resolver list. */
    leakDnsResolvers: string;
  };

  units: {
    ms: string;
  };

  errors: {
    generic: string;
    nameRequired: string;
    urlRequired: string;
    linkRequired: string;
    clipboardEmpty: string;
    clipboardDenied: string;
    qrNotFound: string;
    qrUnsupported: string;
    qrDecodeFailed: string;
    /** No line in the pasted text or chosen file parsed as a valid link. */
    batchEmpty: string;
    /** Import failure causes, shown instead of the raw (URL-bearing) error. */
    importUnreachable: string;
    importBadStatus: string;
    importBadContent: string;
  };
}

const en: Strings = {
  appName: "Tenebra",
  tagline: "Privacy in the dark",
  nav: {
    home: "Home",
    profiles: "Profiles",
    settings: "Settings",
    logs: "Logs",
  },
  state: {
    idle: "Disconnected",
    connecting: "Connecting…",
    connected: "Connected",
    error: "Error",
  },
  topbar: {
    noSubscription: "no subscription",
  },
  conn: {
    eyebrow: "Tunnel status",
    subOff: "traffic unprotected · select a node and connect",
    subPending: "establishing tunnel · negotiating · · ·",
    subConnected: "no logs",
    abort: "ABORT",
    exitIp: "exit node",
    change: "change",
    statSession: "Session",
    statDown: "Down",
    statPing: "Ping",
    unitMinSec: "m:s",
    unitMbps: "Mbps",
  },
  servers: {
    title: "Nodes",
    online: "online",
    showing: "showing",
    regionAll: "all",
    regionEurope: "europe",
    regionAmericas: "americas",
    regionAsiaPac: "asia-pac",
    searchPlaceholder: "search node · de-fra",
    emptyFilter: "no nodes match this filter",
    down: "down",
    addSub: "+ add",
    noNodes: "this subscription has no nodes",
    insecureBadge: "no-cert",
    insecureTitle: "TLS verification off — on-path interception possible",
    insecureSummary:
      "{n} of {m} nodes skip TLS verification — on-path interception possible",
  },
  bottom: {
    routing: "Routing",
    killSwitch: "kill-switch",
    killBanner: "KILL-SWITCH ARMED · traffic blocked if the tunnel drops",
    killSwitchHint:
      "Block traffic that tries to bypass the tunnel; if the tunnel dies, restart it. Applies live; connects get rougher while armed.",
    leakCheck: "leak-check",
    settings: "settings",
  },
  update: {
    available: "Version {version} is available",
    install: "Update",
    later: "Later",
    downloading: "Downloading update…",
  },
  home: {
    connect: "Connect",
    disconnect: "Disconnect",
    cancel: "Cancel",
    quickConnect: "Quick connect",
    noProfile: "No profile yet",
    noProfileHint: "Import a subscription or a link to get started.",
    upload: "Upload",
    download: "Download",
    activeProfile: "Profile",
    activeNode: "Node",
    autoNode: "Auto (lowest ping)",
    sessionTraffic: "This session",
  },
  profiles: {
    title: "Profiles",
    empty: "No profiles",
    emptyHint: "Import a subscription URL, paste a link, or load a file.",
    nodes: "nodes",
    active: "Active",
    setActive: "Set active",
    refresh: "Refresh",
    remove: "Remove",
    removeConfirm: "Remove this profile?",
    pingAll: "Ping all",
    pinging: "Pinging…",
    autoSelect: "Auto-select fastest",
    expires: "Expires",
    expiresIn: "in {days} days",
    expiresToday: "today",
    expiresTomorrow: "tomorrow",
    expired: "expired",
    traffic: "Traffic",
    updated: "Updated",
    source: {
      subscription: "Subscription",
      manual: "Manual",
    },
    import: {
      title: "Import",
      tabSubscription: "Subscription",
      tabLink: "Link",
      tabFile: "File",
      tabQr: "QR code",
      name: "Name",
      namePlaceholder: "My provider",
      url: "Subscription URL",
      urlPlaceholder: "https://…",
      link: "Server link",
      linkPlaceholder: "vless://…  ·  hysteria2://…  ·  ss://…",
      linkHint: "Paste one link, or several — one per line — to import them as a single profile.",
      pickFile: "Choose file…",
      fileHint: "A text file with one server link per line.",
      paste: "Paste",
      qrPick: "Choose image…",
      qrPasteImage: "Paste image",
      qrHint: "An image with a QR code for a subscription or server link.",
      qrScanning: "Scanning…",
      submit: "Import",
      importing: "Importing…",
      batchResult: "Imported {imported}, skipped {skipped}.",
    },
  },
  settings: {
    title: "Settings",
    routing: "Routing",
    routingSmart: "Smart",
    routingSmartHint: "Russian destinations go direct, everything else tunnels.",
    routingGlobal: "Global",
    routingGlobalHint: "Send all traffic through the tunnel.",
    routingDirect: "Direct",
    routingDirectHint: "No tunnel; traffic goes out untouched.",
    split: "Split tunneling",
    splitHint: "Route specific apps differently from the rest, by executable name.",
    splitOff: "Off",
    splitOffHint: "Every app follows the routing above.",
    splitExclude: "Exclude apps",
    splitExcludeHint: "Listed apps bypass the tunnel; everything else stays routed.",
    splitInclude: "Only these apps",
    splitIncludeHint: "Only the listed apps use the tunnel; everything else goes direct.",
    splitApps: "Apps",
    splitAppsEmpty: "No apps yet. Add an executable like chrome.exe.",
    splitAddPlaceholder: "chrome.exe",
    splitAdd: "Add",
    splitRemove: "Remove",
    tunnel: "Tunnel",
    tunnelHint:
      "Which network stack drives the TUN device. Changing it while connected re-applies the tunnel in place (a brief reconnect on the same node).",
    stackSystem: "System",
    stackSystemHint: "The kernel's own TCP/IP. Fastest; the default.",
    stackGvisor: "gVisor",
    stackGvisorHint: "Userspace stack. Slower, but immune to TUN driver quirks.",
    stackMixed: "Mixed",
    stackMixedHint: "TCP on the system stack, UDP on gVisor.",
    appearance: "Appearance",
    theme: "Theme",
    themeDark: "Dark",
    themeLight: "Light",
    language: "Language",
    startup: "Startup",
    launchAtLogin: "Launch at login",
    launchAtLoginHint: "Start Tenebra when you sign in.",
    autoconnect: "Connect on launch",
    autoconnectHint:
      "Reconnect the last used profile when the Tenebra core starts — with the service installed, at system boot.",
    autoFastest: "Auto-select fastest node",
    autoFastestHint:
      "When you connect without picking a node, ping the servers and use the fastest one. Blocked servers are skipped automatically.",
    updates: "Updates",
    updatesCheck: "Check for updates",
    updatesChecking: "Checking…",
    updatesUpToDate: "You're on the latest version.",
    updatesAvailable: "Version {version} is available.",
    updatesDownloading: "Downloading update…",
    updatesInstall: "Install and restart",
    updatesInstalling: "Installing…",
    updatesError: "Couldn't check for updates. Try again later.",
    updatesCurrent: "Current version {version}",
    autoInstall: "Install updates automatically",
    autoInstallHint:
      "When a launch check finds a new version, install it and restart without asking.",
    dns: "DNS",
    dnsHint:
      "Choose the resolvers Tenebra uses, and optionally refuse ad and tracker domains.",
    adBlock: "Block ads and trackers",
    adBlockHint:
      "Refuse DNS lookups for a bundled list of ad and tracker domains. Off by default.",
    dnsRemote: "Encrypted resolver",
    dnsRemoteHint:
      "Resolves general destinations over the tunnel. Leave empty for the default.",
    dnsDirect: "Direct resolver",
    dnsDirectHint:
      "Resolves destinations kept off the tunnel. Leave empty for the default.",
    dnsPlaceholder: "tls://1.1.1.1",
    dnsInvalid:
      "Enter a resolver like tls://1.1.1.1 or https://dns.example/dns-query.",
    updateChannel: "Update channel",
    updateChannelStable: "Stable",
    updateChannelStableHint: "Tested releases only.",
    updateChannelBeta: "Beta",
    updateChannelBetaHint:
      "Get prereleases early, plus every stable release. May be rougher around the edges.",
  },
  logs: {
    title: "Logs",
    empty: "No log entries yet.",
    clear: "Clear",
    leakCheck: "IP / leak check",
    checking: "Checking…",
    leakIpHeading: "Public IP",
    leakDnsHeading: "DNS",
    leakIpOk: "Traffic is leaving through the tunnel exit.",
    leakIpWarn: "Your public IP is not the tunnel exit — traffic may be leaking.",
    leakIpNeutral: "Public IP reported. Not connected, so no exit was checked.",
    leakIpError: "Couldn't determine your public IP from any echo service.",
    egressIp: "Public IP",
    leakExitServer: "Tunnel exit",
    leakSource: "via {source}",
    leakDnsOk: "Resolvers look consistent with the tunnel.",
    leakDnsLeak: "Resolvers appear to bypass the tunnel.",
    leakDnsInconclusive: "Inconclusive — not enough signal for a verdict. This is not a pass.",
    leakDnsUnavailable: "Couldn't run the DNS probe. This is not a pass.",
    leakDnsResolvers: "Resolvers",
  },
  units: {
    ms: "ms",
  },
  errors: {
    generic: "Something went wrong.",
    nameRequired: "Enter a name.",
    urlRequired: "Enter a subscription URL.",
    linkRequired: "Paste a server link.",
    clipboardEmpty: "The clipboard is empty.",
    clipboardDenied: "Couldn't read the clipboard. Grant access or paste manually.",
    qrNotFound: "No QR code found in that image.",
    qrUnsupported: "QR scanning isn't available here. Paste the link instead.",
    qrDecodeFailed: "Couldn't read that image.",
    batchEmpty: "No valid server links found.",
    importUnreachable:
      "Couldn't download the subscription — the server is unreachable. Check your connection or turn on a VPN.",
    importBadStatus: "The subscription server returned an error. Check the link.",
    importBadContent:
      "Couldn't read the subscription — no servers found, or the format isn't supported.",
  },
};

const ru: Strings = {
  appName: "Tenebra",
  tagline: "Приватность в темноте",
  nav: {
    home: "Главная",
    profiles: "Профили",
    settings: "Настройки",
    logs: "Журнал",
  },
  state: {
    idle: "Отключено",
    connecting: "Подключение…",
    connected: "Подключено",
    error: "Ошибка",
  },
  topbar: {
    noSubscription: "нет подписки",
  },
  conn: {
    eyebrow: "Статус туннеля",
    subOff: "трафик не защищён · выберите узел и подключитесь",
    subPending: "поднимаю туннель · согласование · · ·",
    subConnected: "без логов",
    abort: "ОТМЕНА",
    exitIp: "узел выхода",
    change: "сменить",
    statSession: "Сессия",
    statDown: "Загрузка",
    statPing: "Пинг",
    unitMinSec: "м:с",
    unitMbps: "Мбит/с",
  },
  servers: {
    title: "Узлы",
    online: "онлайн",
    showing: "показано",
    regionAll: "все",
    regionEurope: "европа",
    regionAmericas: "америка",
    regionAsiaPac: "азия",
    searchPlaceholder: "поиск узла · de-fra",
    emptyFilter: "нет узлов под этот фильтр",
    down: "недост.",
    addSub: "+ добавить",
    noNodes: "в этой подписке нет узлов",
    insecureBadge: "без серт.",
    insecureTitle: "Проверка TLS отключена — возможен перехват трафика",
    insecureSummary:
      "{n} из {m} узлов без проверки TLS — возможен перехват трафика",
  },
  bottom: {
    routing: "Маршрут",
    killSwitch: "kill-switch",
    killBanner: "KILL-SWITCH ВКЛ · трафик блокируется при обрыве туннеля",
    killSwitchHint:
      "Блокировать трафик в обход туннеля; при падении туннеля — перезапустить его. Применяется сразу; коннект с ним грубее.",
    leakCheck: "проверка",
    settings: "настройки",
  },
  update: {
    available: "Доступна версия {version}",
    install: "Обновить",
    later: "Позже",
    downloading: "Загрузка обновления…",
  },
  home: {
    connect: "Подключиться",
    disconnect: "Отключиться",
    cancel: "Отмена",
    quickConnect: "Быстрое подключение",
    noProfile: "Профилей пока нет",
    noProfileHint: "Импортируйте подписку или ссылку, чтобы начать.",
    upload: "Отдача",
    download: "Загрузка",
    activeProfile: "Профиль",
    activeNode: "Сервер",
    autoNode: "Авто (мин. пинг)",
    sessionTraffic: "За сессию",
  },
  profiles: {
    title: "Профили",
    empty: "Нет профилей",
    emptyHint: "Импортируйте ссылку подписки, вставьте ссылку или загрузите файл.",
    nodes: "серв.",
    active: "Активен",
    setActive: "Сделать активным",
    refresh: "Обновить",
    remove: "Удалить",
    removeConfirm: "Удалить этот профиль?",
    pingAll: "Пинговать все",
    pinging: "Пингую…",
    autoSelect: "Выбрать самый быстрый",
    expires: "Истекает",
    expiresIn: "через {days} дн.",
    expiresToday: "сегодня",
    expiresTomorrow: "завтра",
    expired: "истёк",
    traffic: "Трафик",
    updated: "Обновлён",
    source: {
      subscription: "Подписка",
      manual: "Вручную",
    },
    import: {
      title: "Импорт",
      tabSubscription: "Подписка",
      tabLink: "Ссылка",
      tabFile: "Файл",
      tabQr: "QR-код",
      name: "Название",
      namePlaceholder: "Мой провайдер",
      url: "Ссылка подписки",
      urlPlaceholder: "https://…",
      link: "Ссылка на сервер",
      linkPlaceholder: "vless://…  ·  hysteria2://…  ·  ss://…",
      linkHint: "Вставьте одну ссылку или сразу несколько — по одной в строке — они станут одним профилем.",
      pickFile: "Выбрать файл…",
      fileHint: "Текстовый файл: по одной ссылке на сервер в строке.",
      paste: "Вставить",
      qrPick: "Выбрать изображение…",
      qrPasteImage: "Вставить изображение",
      qrHint: "Изображение с QR-кодом для подписки или ссылки на сервер.",
      qrScanning: "Сканирую…",
      submit: "Импортировать",
      importing: "Импортирую…",
      batchResult: "Импортировано: {imported}, пропущено: {skipped}.",
    },
  },
  settings: {
    title: "Настройки",
    routing: "Маршрутизация",
    routingSmart: "Умная",
    routingSmartHint: "Российские адреса напрямую, остальное через туннель.",
    routingGlobal: "Глобальная",
    routingGlobalHint: "Весь трафик через туннель.",
    routingDirect: "Прямая",
    routingDirectHint: "Без туннеля; трафик идёт напрямую.",
    split: "Раздельный туннель",
    splitHint: "Направлять отдельные приложения иначе, по имени исполняемого файла.",
    splitOff: "Выкл.",
    splitOffHint: "Все приложения следуют маршрутизации выше.",
    splitExclude: "Исключить приложения",
    splitExcludeHint: "Указанные приложения идут мимо туннеля; остальное — как обычно.",
    splitInclude: "Только эти приложения",
    splitIncludeHint: "Через туннель идут только указанные приложения; остальное — напрямую.",
    splitApps: "Приложения",
    splitAppsEmpty: "Пока нет приложений. Добавьте исполняемый файл, например chrome.exe.",
    splitAddPlaceholder: "chrome.exe",
    splitAdd: "Добавить",
    splitRemove: "Удалить",
    tunnel: "Туннель",
    tunnelHint:
      "Какой сетевой стек обслуживает TUN-устройство. Смена при живом туннеле применяется на месте (короткий реконнект на тот же узел).",
    stackSystem: "Системный",
    stackSystemHint: "TCP/IP ядра ОС. Самый быстрый; по умолчанию.",
    stackGvisor: "gVisor",
    stackGvisorHint: "Стек в user space. Медленнее, но не зависит от капризов TUN-драйвера.",
    stackMixed: "Смешанный",
    stackMixedHint: "TCP через системный стек, UDP через gVisor.",
    appearance: "Оформление",
    theme: "Тема",
    themeDark: "Тёмная",
    themeLight: "Светлая",
    language: "Язык",
    startup: "Запуск",
    launchAtLogin: "Запускать при входе",
    launchAtLoginHint: "Открывать Tenebra при входе в систему.",
    autoconnect: "Подключаться при запуске",
    autoconnectHint:
      "Восстанавливать последний профиль при старте ядра Tenebra — со службой это происходит при загрузке системы.",
    autoFastest: "Выбирать самый быстрый узел",
    autoFastestHint:
      "При подключении без выбора узла пинговать серверы и использовать самый быстрый. Заблокированные серверы пропускаются автоматически.",
    updates: "Обновления",
    updatesCheck: "Проверить обновление",
    updatesChecking: "Проверяю…",
    updatesUpToDate: "Установлена последняя версия.",
    updatesAvailable: "Доступна версия {version}.",
    updatesDownloading: "Загрузка обновления…",
    updatesInstall: "Установить и перезапустить",
    updatesInstalling: "Устанавливаю…",
    updatesError: "Не удалось проверить обновление. Попробуйте позже.",
    updatesCurrent: "Текущая версия {version}",
    autoInstall: "Устанавливать обновления автоматически",
    autoInstallHint:
      "Если проверка при запуске нашла новую версию — установить её и перезапустить без вопросов.",
    dns: "DNS",
    dnsHint:
      "Выберите резолверы, которые использует Tenebra, и при желании отклоняйте рекламные и трекерные домены.",
    adBlock: "Блокировать рекламу и трекеры",
    adBlockHint:
      "Отклонять DNS-запросы к встроенному списку рекламных и трекерных доменов. По умолчанию выключено.",
    dnsRemote: "Шифрованный резолвер",
    dnsRemoteHint:
      "Резолвит обычные адреса через туннель. Оставьте пустым для значения по умолчанию.",
    dnsDirect: "Прямой резолвер",
    dnsDirectHint:
      "Резолвит адреса, идущие мимо туннеля. Оставьте пустым для значения по умолчанию.",
    dnsPlaceholder: "tls://1.1.1.1",
    dnsInvalid:
      "Введите резолвер вида tls://1.1.1.1 или https://dns.example/dns-query.",
    updateChannel: "Канал обновлений",
    updateChannelStable: "Стабильный",
    updateChannelStableHint: "Только проверенные релизы.",
    updateChannelBeta: "Бета",
    updateChannelBetaHint:
      "Ранний доступ к предрелизам и все стабильные версии. Может быть сырее.",
  },
  logs: {
    title: "Журнал",
    empty: "Записей пока нет.",
    clear: "Очистить",
    leakCheck: "Проверка IP / утечек",
    checking: "Проверяю…",
    leakIpHeading: "Внешний IP",
    leakDnsHeading: "DNS",
    leakIpOk: "Трафик выходит через узел туннеля.",
    leakIpWarn: "Ваш внешний IP не совпадает с узлом туннеля — возможна утечка.",
    leakIpNeutral: "Внешний IP получен. Подключения нет, узел не проверялся.",
    leakIpError: "Не удалось определить внешний IP ни через один сервис.",
    egressIp: "Внешний IP",
    leakExitServer: "Узел туннеля",
    leakSource: "через {source}",
    leakDnsOk: "Резолверы выглядят согласованными с туннелем.",
    leakDnsLeak: "Похоже, резолверы идут мимо туннеля.",
    leakDnsInconclusive: "Неопределённо — данных для вердикта недостаточно. Это не гарантия.",
    leakDnsUnavailable: "Не удалось выполнить проверку DNS. Это не гарантия.",
    leakDnsResolvers: "Резолверы",
  },
  units: {
    ms: "мс",
  },
  errors: {
    generic: "Что-то пошло не так.",
    nameRequired: "Введите название.",
    urlRequired: "Введите ссылку подписки.",
    linkRequired: "Вставьте ссылку на сервер.",
    clipboardEmpty: "Буфер обмена пуст.",
    clipboardDenied: "Не удалось прочитать буфер обмена. Дайте доступ или вставьте вручную.",
    qrNotFound: "В этом изображении нет QR-кода.",
    qrUnsupported: "Сканирование QR здесь недоступно. Вставьте ссылку вручную.",
    qrDecodeFailed: "Не удалось прочитать изображение.",
    batchEmpty: "Не найдено ни одной корректной ссылки на сервер.",
    importUnreachable:
      "Не удалось скачать подписку — сервер недоступен. Проверьте соединение или включите VPN.",
    importBadStatus: "Сервер подписки ответил ошибкой. Проверьте ссылку.",
    importBadContent:
      "Не удалось разобрать подписку — серверы не найдены или формат не поддерживается.",
  },
};

export const dictionaries: Record<Language, Strings> = { en, ru };
