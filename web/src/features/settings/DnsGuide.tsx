import clsx from "clsx";
import { Check, Copy } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { useProjects } from "@/api/hooks";
import { Button } from "@/components/ui";
import { copyText } from "@/lib/clipboard";

const ipv4 = /^(25[0-5]|2[0-4]\d|1?\d?\d)(\.(25[0-5]|2[0-4]\d|1?\d?\d)){3}$/;

/**
 * dnsTarget picks the address the wildcard entry must point at: Envoryx's own IP (macvlan,
 * br0), else the configured host for project links, else the address this page was opened
 * on – as long as it is an IPv4 address, because every resolver below wants one.
 */
export function dnsTarget(proxyAddress: string | undefined, publicHost: string, pageHost: string): string {
  for (const candidate of [proxyAddress ?? "", publicHost, pageHost]) {
    if (ipv4.test(candidate)) return candidate;
  }
  return "";
}

function Snippet({ text }: { text: string }) {
  const { t } = useTranslation();
  const [copied, setCopied] = useState(false);
  return (
    <div className="flex items-start gap-2">
      <pre className="min-w-0 flex-1 overflow-x-auto rounded-md border border-default bg-muted px-3 py-2 font-mono text-[12px] text-fg">{text}</pre>
      <Button
        type="button"
        size="sm"
        variant="ghost"
        onClick={async () => {
          setCopied(await copyText(text));
          setTimeout(() => setCopied(false), 1500);
        }}
        icon={copied ? <Check className="size-3.5 text-emerald-500" /> : <Copy className="size-3.5" />}
      >
        {t("Copy")}
      </Button>
    </div>
  );
}

type Guide = { id: string; label: string; steps: string[]; snippets: string[] };

/** DnsGuide shows, per DNS server, the one wildcard entry that sends *.<base> to Envoryx. */
export function DnsGuide({ baseDomain, target }: { baseDomain: string; target: string }) {
  const { t } = useTranslation();
  const projects = useProjects();
  const [active, setActive] = useState("adguard");
  const base = baseDomain || "test";
  const ip = target || "<server-ip>";
  const vars = { base, ip };

  const hostnames = new Set([`envoryx.${base}`]);
  for (const p of projects.data ?? []) {
    for (const h of p.hostnames) hostnames.add(h);
    if (p.devHostname) hostnames.add(p.devHostname);
  }

  const guides: Guide[] = [
    {
      id: "adguard",
      label: "AdGuard Home",
      steps: [t("Filters → DNS rewrites → Add DNS rewrite. Domain: *.{{base}}, answer: {{ip}}.", vars)],
      snippets: [],
    },
    {
      id: "pihole",
      label: "Pi-hole",
      steps: [
        t("Local DNS records cannot hold a wildcard, so the entry goes into dnsmasq, which Pi-hole is built on."),
        t("Pi-hole 6: Settings → All settings (Expert mode) → Miscellaneous → misc.dnsmasq_lines, add the line, then Save & Apply."),
        t("Pi-hole 5: put the line into /etc/dnsmasq.d/99-envoryx.conf and run pihole restartdns."),
      ],
      snippets: [`address=/${base}/${ip}`],
    },
    {
      id: "dnsmasq",
      label: "dnsmasq / OpenWrt",
      steps: [t("Add the line to the dnsmasq configuration and restart dnsmasq. On OpenWrt the command below does both.")],
      snippets: [`address=/${base}/${ip}`, `uci add_list dhcp.@dnsmasq[0].address='/${base}/${ip}' && uci commit dhcp && /etc/init.d/dnsmasq restart`],
    },
    {
      id: "unbound",
      label: "Unbound",
      steps: [t("pfSense: Services → DNS Resolver → Custom options. OPNsense and standalone Unbound: a .conf file in the include directory (OPNsense: /usr/local/etc/unbound.opnsense.d/), then restart Unbound.")],
      snippets: [`server:\n  local-zone: "${base}." redirect\n  local-data: "${base}. IN A ${ip}"`],
    },
    {
      id: "hosts",
      label: t("Hosts file"),
      steps: [
        t("For routers that cannot do wildcard entries, such as a FritzBox: one line in the hosts file of every device. Each new project or domain needs to be added by hand."),
        t("Linux and macOS: /etc/hosts. Windows: C:\\Windows\\System32\\drivers\\etc\\hosts (as administrator)."),
      ],
      snippets: [`${ip} ${[...hostnames].join(" ")}`],
    },
  ];
  const guide = guides.find((g) => g.id === active) ?? guides[0]!;

  return (
    <div className="border-t border-default pt-5">
      <h3 className="text-sm font-semibold">{t("DNS for *.{{base}}", vars)}</h3>
      <p className="mt-1 text-sm text-muted">
        {t("One wildcard entry in the DNS server of your network points every name under {{base}} at {{ip}} – current and future projects alike.", vars)}
        {!target && " " + t("Replace <server-ip> with the address of the Docker host.")}
      </p>
      <div className="mt-3 flex flex-wrap gap-1" role="tablist" aria-label={t("DNS server")}>
        {guides.map((g) => (
          <button
            key={g.id}
            type="button"
            role="tab"
            aria-selected={g.id === guide.id}
            onClick={() => setActive(g.id)}
            className={clsx("rounded-md px-2.5 py-1.5 text-xs font-medium", g.id === guide.id ? "bg-accent-500/10 text-accent-600 dark:text-accent-300" : "text-muted hover:bg-muted hover:text-fg")}
          >
            {g.label}
          </button>
        ))}
      </div>
      <div className="mt-3 space-y-2 text-sm" role="tabpanel">
        {guide.steps.map((s) => (
          <p key={s}>{s}</p>
        ))}
        {guide.snippets.map((s) => (
          <Snippet key={s} text={s} />
        ))}
      </div>
      <p className="mt-3 text-xs text-muted">
        {t("If the Docker host uses the same DNS server, project containers resolve these names too, for example when an application calls its own URL. Settings → Diagnostics checks both.")}
      </p>
    </div>
  );
}
