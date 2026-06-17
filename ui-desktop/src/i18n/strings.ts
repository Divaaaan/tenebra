// Bilingual UI strings. The two dictionaries share one shape so a missing key is
// a compile error rather than a silent fallback. Interpolation is done by the
// caller; values here are plain strings.

export type Language = "en" | "ru";

export interface Strings {
  appName: string;

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
      pickFile: string;
      fileHint: string;
      paste: string;
      qrPick: string;
      qrPasteImage: string;
      qrHint: string;
      qrScanning: string;
      submit: string;
      importing: string;
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
  };

  logs: {
    title: string;
    empty: string;
    clear: string;
    leakCheck: string;
    checking: string;
    leakResultTunneled: string;
    leakResultExposed: string;
    egressIp: string;
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
  };
}

const en: Strings = {
  appName: "Tenebra",
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
      pickFile: "Choose file…",
      fileHint: "A text file with one server link per line.",
      paste: "Paste",
      qrPick: "Choose image…",
      qrPasteImage: "Paste image",
      qrHint: "An image with a QR code for a subscription or server link.",
      qrScanning: "Scanning…",
      submit: "Import",
      importing: "Importing…",
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
  },
  logs: {
    title: "Logs",
    empty: "No log entries yet.",
    clear: "Clear",
    leakCheck: "IP / leak check",
    checking: "Checking…",
    leakResultTunneled: "Traffic is going through the tunnel.",
    leakResultExposed: "Egress matches your real address — not tunneled.",
    egressIp: "Egress IP",
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
  },
};

const ru: Strings = {
  appName: "Tenebra",
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
      pickFile: "Выбрать файл…",
      fileHint: "Текстовый файл: по одной ссылке на сервер в строке.",
      paste: "Вставить",
      qrPick: "Выбрать изображение…",
      qrPasteImage: "Вставить изображение",
      qrHint: "Изображение с QR-кодом для подписки или ссылки на сервер.",
      qrScanning: "Сканирую…",
      submit: "Импортировать",
      importing: "Импортирую…",
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
  },
  logs: {
    title: "Журнал",
    empty: "Записей пока нет.",
    clear: "Очистить",
    leakCheck: "Проверка IP / утечек",
    checking: "Проверяю…",
    leakResultTunneled: "Трафик идёт через туннель.",
    leakResultExposed: "Адрес совпадает с вашим реальным — туннель не работает.",
    egressIp: "Внешний IP",
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
  },
};

export const dictionaries: Record<Language, Strings> = { en, ru };
