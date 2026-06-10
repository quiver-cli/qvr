import {
  createContext,
  useCallback,
  useContext,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { CheckCircle2, AlertCircle, Info, X, Zap } from "lucide-react";

type ToastTone = "accent" | "success" | "danger" | "info";

interface ToastMsg {
  id: number;
  tone: ToastTone;
  title: string;
  body?: string;
}

interface ToastAPI {
  push: (t: { tone?: ToastTone; title: string; body?: string }) => void;
}

const ToastCtx = createContext<ToastAPI>({ push: () => {} });

// useToast exposes push() — fire-and-forget notifications (copy confirmations,
// live-scan results) that stack bottom-right and auto-dismiss.
export function useToast(): ToastAPI {
  return useContext(ToastCtx);
}

const toneIcon: Record<ToastTone, ReactNode> = {
  accent: <Zap size={16} />,
  success: <CheckCircle2 size={16} />,
  danger: <AlertCircle size={16} />,
  info: <Info size={16} />,
};

const TOAST_TTL_MS = 4200;

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<ToastMsg[]>([]);
  const nextId = useRef(1);

  const dismiss = useCallback((id: number) => {
    setToasts((ts) => ts.filter((t) => t.id !== id));
  }, []);

  const push = useCallback(
    ({ tone = "accent", title, body }: { tone?: ToastTone; title: string; body?: string }) => {
      const id = nextId.current++;
      setToasts((ts) => [...ts, { id, tone, title, body }]);
      setTimeout(() => dismiss(id), TOAST_TTL_MS);
    },
    [dismiss],
  );

  const apiValue = useMemo(() => ({ push }), [push]);

  return (
    <ToastCtx.Provider value={apiValue}>
      {children}
      <div className="qvr-toastvp" data-theme="light">
        {toasts.map((t) => (
          <div key={t.id} className={`qvr-toast qvr-toast--${t.tone}`} role="status">
            <span className="qvr-toast__icon">{toneIcon[t.tone]}</span>
            <div className="qvr-toast__body">
              <p className="qvr-toast__title">{t.title}</p>
              {t.body && <p className="qvr-toast__msg">{t.body}</p>}
            </div>
            <button
              type="button"
              className="qvr-toast__close"
              aria-label="dismiss"
              onClick={() => dismiss(t.id)}
            >
              <X size={14} />
            </button>
          </div>
        ))}
      </div>
    </ToastCtx.Provider>
  );
}
