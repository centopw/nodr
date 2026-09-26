export interface StatusBadgeProps {
  powerState: string;
}

const KNOWN_STATES: Record<string, true> = {
  running: true,
  stopped: true,
  unmanaged: true,
};

export default function StatusBadge({ powerState }: StatusBadgeProps) {
  const normalized = (powerState || "").toLowerCase();
  const known = KNOWN_STATES[normalized] === true;
  const state = known ? normalized : "unmanaged";
  const label = known ? normalized : powerState || "unmanaged";

  return <span className={`status-badge status-${state}`}>{label}</span>;
}
