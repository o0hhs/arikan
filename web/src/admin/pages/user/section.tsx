

export function Section({ title, aside, children }: { title: string; aside?: React.ReactNode; children: React.ReactNode }) {
  return (
    <section className="dr-sec">
      <h3>
        {title}
        {aside ? <span className="font-normal text-[var(--md-on-surface-variant)]">{aside}</span> : null}
      </h3>
      {children}
    </section>
  );
}

export function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div>
      {label}
      <b className="num mt-0.5 block text-[15px] leading-5 font-medium text-[var(--md-on-surface)]">{value}</b>
    </div>
  );
}
