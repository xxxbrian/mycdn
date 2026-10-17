import {
  AlertCircle,
  ArrowDown,
  ArrowUp,
  Check,
  GripVertical,
  Loader2,
  Plus,
  RefreshCw,
  X,
} from 'lucide-react';
import { useId, useState } from 'react';
import type {
  ButtonHTMLAttributes,
  InputHTMLAttributes,
  ReactNode,
  SelectHTMLAttributes,
  TextareaHTMLAttributes,
  Ref,
} from 'react';

export function Button({
  children,
  variant = 'default',
  className = '',
  ...props
}: ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: 'default' | 'primary' | 'danger' | 'ghost';
}) {
  return (
    <button type="button" className={`button ${variant} ${className}`} {...props}>
      {children}
    </button>
  );
}
export function IconButton({
  label,
  children,
  ...props
}: ButtonHTMLAttributes<HTMLButtonElement> & { label: string }) {
  return (
    <Button aria-label={label} title={label} className="icon-button" {...props}>
      {children}
    </Button>
  );
}
export function Panel({
  title,
  eyebrow,
  subtitle,
  action,
  children,
  className = '',
}: {
  title?: string;
  eyebrow?: string;
  subtitle?: string;
  action?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  return (
    <section className={`panel ${className}`}>
      {(title || action) && (
        <header className="panel-heading">
          <div>
            {eyebrow && <span className="eyebrow">{eyebrow}</span>}
            <h2>{title}</h2>
            {subtitle && <p>{subtitle}</p>}
          </div>
          {action}
        </header>
      )}
      <div className="panel-body">{children}</div>
    </section>
  );
}
export function Field({
  label,
  hint,
  children,
  className = '',
}: {
  label: string;
  hint?: string;
  children: ReactNode;
  className?: string;
}) {
  return (
    <label className={`field ${className}`}>
      <span className="field-label">{label}</span>
      {children}
      {hint && <span className="field-hint">{hint}</span>}
    </label>
  );
}
export function Input(props: InputHTMLAttributes<HTMLInputElement>) {
  return <input {...props} className={`input ${props.className || ''}`} />;
}
export function Textarea(
  props: TextareaHTMLAttributes<HTMLTextAreaElement> & { ref?: Ref<HTMLTextAreaElement> },
) {
  return <textarea rows={3} {...props} className={`input textarea ${props.className || ''}`} />;
}
export function Select(props: SelectHTMLAttributes<HTMLSelectElement>) {
  return <select {...props} className={`input select ${props.className || ''}`} />;
}
export function CheckField({
  label,
  checked,
  onChange,
  hint,
}: {
  label: string;
  checked: boolean;
  onChange: (value: boolean) => void;
  hint?: string;
}) {
  return (
    <label className="check-field">
      <input
        type="checkbox"
        checked={checked}
        onChange={(event) => onChange(event.target.checked)}
      />
      <span>
        <span>{label}</span>
        {hint && <small>{hint}</small>}
      </span>
    </label>
  );
}
export function Alert({
  children,
  tone = 'error',
  action,
}: {
  children: ReactNode;
  tone?: 'error' | 'warning' | 'success' | 'info';
  action?: ReactNode;
}) {
  return (
    <div role={tone === 'error' ? 'alert' : 'status'} className={`alert ${tone}`}>
      {tone === 'success' ? <Check size={16} /> : <AlertCircle size={16} />}
      <div>{children}</div>
      {action}
    </div>
  );
}
export function Empty({
  title = 'No data in this range',
  description = 'Try a wider time range or remove a filter.',
  icon,
}: {
  title?: string;
  description?: string;
  icon?: ReactNode;
}) {
  return (
    <div className="empty">
      {icon}
      <strong>{title}</strong>
      <span>{description}</span>
    </div>
  );
}
export function Loading({ label = 'Loading…' }: { label?: string }) {
  return (
    <div role="status" className="loading">
      <Loader2 size={17} className="spin" />
      {label}
    </div>
  );
}
export function LoadError({ error, retry }: { error: string; retry?: () => void }) {
  return (
    <Alert
      action={
        retry && (
          <Button onClick={retry}>
            <RefreshCw size={14} />
            Retry
          </Button>
        )
      }
    >
      {error}
    </Alert>
  );
}
export function Badge({ children, tone = '' }: { children: ReactNode; tone?: string }) {
  return <span className={`badge ${tone}`}>{children}</span>;
}
export function Metric({
  label,
  value,
  note,
  accent = false,
  icon,
}: {
  label: string;
  value: string;
  note?: string;
  accent?: boolean;
  icon?: ReactNode;
}) {
  return (
    <div className={`metric ${accent ? 'accent' : ''}`}>
      <div className="metric-label">
        {label}
        {icon}
      </div>
      <strong className="metric-value mono">{value}</strong>
      {note && <span className="metric-note">{note}</span>}
    </div>
  );
}
export function PageHeading({
  title,
  description,
  eyebrow,
  action,
}: {
  title: string;
  description: string;
  eyebrow?: string;
  action?: ReactNode;
}) {
  return (
    <div className="page-heading">
      <div>
        <span className="eyebrow">{eyebrow || 'EDGE CONSOLE'}</span>
        <h1>{title}</h1>
        <p>{description}</p>
      </div>
      {action}
    </div>
  );
}
export function AddButton({
  children,
  onClick,
  disabled,
}: {
  children: ReactNode;
  onClick: () => void;
  disabled?: boolean;
}) {
  return (
    <Button onClick={onClick} disabled={disabled}>
      <Plus size={14} />
      {children}
    </Button>
  );
}
export function Tabs({
  items,
  value,
  onChange,
  label = 'View',
}: {
  items: { id: string; label: string; count?: number }[];
  value: string;
  onChange: (id: string) => void;
  label?: string;
}) {
  return (
    <div className="tabs" role="tablist" aria-label={label}>
      {items.map((item) => (
        <button
          type="button"
          key={item.id}
          role="tab"
          aria-selected={value === item.id}
          tabIndex={value === item.id ? 0 : -1}
          className={value === item.id ? 'active' : ''}
          onClick={() => onChange(item.id)}
          onKeyDown={(event) => {
            const index = items.findIndex((entry) => entry.id === item.id);
            const target =
              event.key === 'ArrowRight'
                ? (index + 1) % items.length
                : event.key === 'ArrowLeft'
                  ? (index - 1 + items.length) % items.length
                  : event.key === 'Home'
                    ? 0
                    : event.key === 'End'
                      ? items.length - 1
                      : -1;
            if (target === -1) return;
            event.preventDefault();
            onChange(items[target].id);
            (
              event.currentTarget.parentElement?.children[target] as HTMLButtonElement | undefined
            )?.focus();
          }}
        >
          {item.label}
          {item.count !== undefined && <span className="tab-count mono">{item.count}</span>}
        </button>
      ))}
    </div>
  );
}
export function Disclosure({
  title,
  children,
  open = false,
}: {
  title: string;
  children: ReactNode;
  open?: boolean;
}) {
  return (
    <details className="disclosure" open={open || undefined}>
      <summary>{title}</summary>
      <div>{children}</div>
    </details>
  );
}
export function ConfirmButton({
  children,
  confirm,
  onConfirm,
  variant = 'danger',
  disabled,
}: {
  children: ReactNode;
  confirm: string;
  onConfirm: () => void;
  variant?: 'danger' | 'default';
  disabled?: boolean;
}) {
  const [armed, setArmed] = useState(false);
  return armed ? (
    <span className="inline-actions">
      <Button
        variant="danger"
        disabled={disabled}
        onClick={() => {
          setArmed(false);
          onConfirm();
        }}
      >
        {confirm}
      </Button>
      <IconButton label="Cancel" onClick={() => setArmed(false)}>
        <X size={14} />
      </IconButton>
    </span>
  ) : (
    <Button variant={variant} disabled={disabled} onClick={() => setArmed(true)}>
      {children}
    </Button>
  );
}

// Native drag is confined to this sibling list. Keyboard buttons always provide
// the same reorder operation; no global tree drag or invalid cross-level moves.
export function SortableList<T extends { id: string }>({
  items,
  onChange,
  render,
  label,
}: {
  items: T[];
  onChange: (items: T[]) => void;
  render: (item: T, index: number) => ReactNode;
  label: string;
}) {
  const scope = useId();
  const transferType = `application/x-mycdn-${scope.replace(/[^a-z0-9]/gi, '').toLowerCase()}`;
  const [drag, setDrag] = useState<number | null>(null);
  const [over, setOver] = useState<number | null>(null);
  const [announcement, setAnnouncement] = useState('');
  function move(from: number, to: number) {
    if (from === to || from < 0 || to < 0 || to >= items.length) return;
    const next = [...items];
    next.splice(to, 0, next.splice(from, 1)[0]);
    onChange(next);
    setAnnouncement(`${label} moved from position ${from + 1} to ${to + 1}`);
  }
  return (
    <>
      <span className="sr-only" aria-live="polite">
        {announcement}
      </span>
      <div className="sortable-list">
        {items.map((item, index) => (
          <div
            key={item.id}
            className={`sortable-item ${over === index ? 'drag-over' : ''}`}
            onDragOver={(event) => {
              if (drag === null || !event.dataTransfer.types.includes(transferType)) return;
              event.preventDefault();
              event.stopPropagation();
              setOver(index);
            }}
            onDrop={(event) => {
              if (drag === null || !event.dataTransfer.types.includes(transferType)) return;
              event.preventDefault();
              event.stopPropagation();
              move(drag, index);
              setDrag(null);
              setOver(null);
            }}
          >
            <div className="sort-controls">
              <button
                type="button"
                className="drag-handle"
                aria-label={`Drag ${label} ${index + 1}`}
                draggable
                onDragStart={(event) => {
                  event.stopPropagation();
                  event.dataTransfer.setData(transferType, item.id);
                  event.dataTransfer.effectAllowed = 'move';
                  setDrag(index);
                }}
                onDragEnd={() => {
                  setDrag(null);
                  setOver(null);
                }}
              >
                <GripVertical size={15} />
              </button>
              <IconButton
                label={`Move ${label} ${index + 1} up`}
                disabled={index === 0}
                onClick={() => move(index, index - 1)}
              >
                <ArrowUp size={12} />
              </IconButton>
              <IconButton
                label={`Move ${label} ${index + 1} down`}
                disabled={index === items.length - 1}
                onClick={() => move(index, index + 1)}
              >
                <ArrowDown size={12} />
              </IconButton>
            </div>
            <div className="sort-content">{render(item, index)}</div>
          </div>
        ))}
      </div>
    </>
  );
}
