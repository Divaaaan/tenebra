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
  };

  /** Bottom bar: routing segmented control, kill-switch, quick actions. */
  bottom: {
    routing: string;
    killSwitch: string;
    killBanner: string;
    /** Tooltip while the kill-switch is present but not yet core-enforced. */
    killSwitchPending: string;
    leakCheck: string;
    settings: string;
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
    perSecond: string;
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
  },
  bottom: {
    routing: "Routing",
    killSwitch: "kill-switch",
    killBanner: "KILL-SWITCH ARMED · traffic blocked if the tunnel drops",
    killSwitchPending: "Kill-switch isn't wired to the core yet",
    leakCheck: "leak-check",
    settings: "settings",
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
    appearance: "Appearance",
    theme: "Theme",
    themeDark: "Dark",
    themeLight: "Light",
    language: "Language",
    startup: "Startup",
    launchAtLogin: "Launch at login",
    launchAtLoginHint: "Start Tenebra when you sign in.",
    autoconnect: "Connect on launch",
    autoconnectHint: "Reconnect the last used profile when Tenebra starts.",
    autoFastest: "Auto-select fastest node",
    autoFastestHint:
      "When you connect without picking a node, ping the servers and use the fastest one. Blocked servers are skipped automatically.",
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
    perSecond: "/s",
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
  },
  bottom: {
    routing: "Маршрут",
    killSwitch: "kill-switch",
    killBanner: "KILL-SWITCH ВКЛ · трафик блокируется при обрыве туннеля",
    killSwitchPending: "Kill-switch ещё не подключён к ядру",
    leakCheck: "проверка",
    settings: "настройки",
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
    appearance: "Оформление",
    theme: "Тема",
    themeDark: "Тёмная",
    themeLight: "Светлая",
    language: "Язык",
    startup: "Запуск",
    launchAtLogin: "Запускать при входе",
    launchAtLoginHint: "Открывать Tenebra при входе в систему.",
    autoconnect: "Подключаться при запуске",
    autoconnectHint: "Восстанавливать последний профиль при старте Tenebra.",
    autoFastest: "Выбирать самый быстрый узел",
    autoFastestHint:
      "При подключении без выбора узла пинговать серверы и использовать самый быстрый. Заблокированные серверы пропускаются автоматически.",
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
    perSecond: "/с",
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
  },
};

export const dictionaries: Record<Language, Strings> = { en, ru };
