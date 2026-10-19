import React, { useEffect, useId, useRef, useState } from 'react';
import { Check } from 'lucide-react';

export interface MenuItem {
  id: string | number;
  label: string;
  hint?: string;
  selected?: boolean;
  onSelect: () => void;
}

interface MenuProps {
  label: string;
  title?: string;
  icon: React.ReactNode;
  items: MenuItem[];
  disabled?: boolean;
  active?: boolean;
  // Shown beside the icon on wide control bars, for example the current speed.
  caption?: string;
}

// A control-bar button that opens a list of single-choice options above
// itself. Outside pointer presses and Escape close it; the list is a menu
// for assistive technology and each row says whether it is the active one.
export const Menu: React.FC<MenuProps> = ({ label, title, icon, items, disabled, active, caption }) => {
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLDivElement | null>(null);
  const listId = useId();

  useEffect(() => {
    if (!open) return undefined;
    const onPointerDown = (event: PointerEvent) => {
      if (rootRef.current && !rootRef.current.contains(event.target as Node)) setOpen(false);
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.stopPropagation();
        setOpen(false);
      }
    };
    document.addEventListener('pointerdown', onPointerDown);
    document.addEventListener('keydown', onKeyDown, true);
    return () => {
      document.removeEventListener('pointerdown', onPointerDown);
      document.removeEventListener('keydown', onKeyDown, true);
    };
  }, [open]);

  return (
    <div ref={rootRef} className="mp-menu">
      <button
        type="button"
        className={`mp-btn ${active ? 'mp-btn--active' : ''}`}
        aria-label={label}
        title={title ?? label}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? listId : undefined}
        disabled={disabled || items.length === 0}
        onClick={() => setOpen((value) => !value)}
      >
        {icon}
        {caption && <span className="mp-btn-caption">{caption}</span>}
      </button>
      {open && (
        <ul id={listId} className="mp-menu-list" role="menu" aria-label={label}>
          {items.map((item) => (
            <li key={item.id} role="none">
              <button
                type="button"
                role="menuitemradio"
                aria-checked={item.selected === true}
                className={`mp-menu-item ${item.selected ? 'mp-menu-item--selected' : ''}`}
                onClick={() => {
                  item.onSelect();
                  setOpen(false);
                }}
              >
                <span className="mp-menu-check" aria-hidden="true">{item.selected && <Check size={13} />}</span>
                <span className="mp-menu-label">{item.label}</span>
                {item.hint && <span className="mp-menu-hint">{item.hint}</span>}
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
};
