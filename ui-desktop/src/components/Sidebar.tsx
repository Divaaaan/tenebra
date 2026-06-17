import type { ReactNode } from "react";

import type { ScreenId } from "../App";
import type { State } from "../api";
import { useI18n } from "../i18n/I18nContext";
import { StatusPill } from "./StatusPill";

interface SidebarProps {
  active: ScreenId;
  onNavigate: (screen: ScreenId) => void;
  state: State;
}

const ICONS: Record<ScreenId, ReactNode> = {
  home: (
    <path d="M3 10.5 12 3l9 7.5M5 9.5V20h5v-5h4v5h5V9.5" />
  ),
  profiles: (
    <>
      <rect x="3" y="4" width="18" height="5" rx="1.5" />
      <rect x="3" y="11" width="18" height="5" rx="1.5" />
      <path d="M7 6.5h.01M7 13.5h.01" />
    </>
  ),
  settings: (
    <>
      <circle cx="12" cy="12" r="3" />
      <path d="M12 2v3m0 14v3M4.2 4.2l2.1 2.1m11.4 11.4 2.1 2.1M2 12h3m14 0h3M4.2 19.8l2.1-2.1m11.4-11.4 2.1-2.1" />
    </>
  ),
  logs: (
    <>
      <rect x="4" y="3" width="16" height="18" rx="2" />
      <path d="M8 8h8M8 12h8M8 16h5" />
    </>
  ),
};

const ORDER: ScreenId[] = ["home", "profiles", "settings", "logs"];

export function Sidebar({ active, onNavigate, state }: SidebarProps) {
  const { t } = useI18n();

  return (
    <aside className="app-sidebar">
      <div className="sidebar-brand">
        <span className="sidebar-mark" aria-hidden="true">
          T
        </span>
        <span className="sidebar-title">{t.appName}</span>
      </div>

      <nav className="sidebar-nav" aria-label={t.appName}>
        {ORDER.map((id) => {
          const isActive = id === active;
          return (
            <button
              key={id}
              type="button"
              className={`sidebar-link${isActive ? " active" : ""}`}
              aria-current={isActive ? "page" : undefined}
              onClick={() => onNavigate(id)}
            >
              <svg
                className="sidebar-icon"
                viewBox="0 0 24 24"
                width="18"
                height="18"
                fill="none"
                stroke="currentColor"
                strokeWidth="1.8"
                strokeLinecap="round"
                strokeLinejoin="round"
                aria-hidden="true"
              >
                {ICONS[id]}
              </svg>
              {t.nav[id]}
            </button>
          );
        })}
      </nav>

      <div className="sidebar-foot">
        <StatusPill state={state.state} />
      </div>
    </aside>
  );
}
