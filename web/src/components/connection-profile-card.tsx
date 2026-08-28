import type { ReactNode } from "react";

import { CopyButton } from "@/components/copy-button";
import { QrCode } from "@/components/qr-code";
import type {
  AvailableConnectionProfile,
  UnavailableConnectionProfile,
  UserDetail,
} from "@/lib/api";
import { securityValues, transportValues, type TypedValue } from "@/lib/profile-fields";
import { cn } from "@/lib/utils";

// The Connection profiles section of the User details dialog (IN-DEV-SPEC
// §7.3): one card per matching VLESS inbound, in xray inbound order, with
// profile freshness reported separately from observation freshness.
export function ConnectionProfiles({
  profiles,
  refreshFailed,
}: {
  profiles: UserDetail["connection_profiles"];
  refreshFailed: boolean;
}) {
  return (
    <section aria-labelledby="connection-profiles-heading" className="mt-6">
      <div className="mb-2.5 flex items-baseline justify-between gap-3">
        <h3 id="connection-profiles-heading" className="text-[13px] font-semibold">
          Connection profiles
        </h3>
        <span
          className={cn("text-[11px]", profiles.stale ? "text-warning" : "text-muted-foreground")}
        >
          {refreshFailed
            ? profiles.stale
              ? "stale profile sources · refresh failed"
              : "refresh failed · showing previous profiles"
            : profiles.stale
              ? "stale profile sources"
              : profiles.loaded_at === null
                ? "profile sources unavailable"
                : "current profile sources"}
        </span>
      </div>

      {profiles.errors.length > 0 ? (
        <div
          role="alert"
          className="border-warning/35 bg-warning/10 text-warning mb-3 rounded-[10px] border px-3.5 py-3 text-xs"
        >
          <strong>Profile source warning</strong>
          <ul className="mt-1.5 grid gap-1">
            {profiles.errors.map((sourceError) => (
              <li key={`${sourceError.source}:${sourceError.reason}`}>
                <code>{sourceError.source}</code> · <code>{sourceError.reason}</code>:{" "}
                {sourceError.message}
              </li>
            ))}
          </ul>
        </div>
      ) : null}

      {profiles.state === "ready" ? (
        profiles.items.length > 0 ? (
          // xray inbound order, available and unavailable alike — the position
          // is what ties a card to an inbound the admin can find.
          <div className="grid gap-3">
            {profiles.items.map((profile, index) =>
              profile.status === "available" ? (
                <AvailableProfileCard
                  key={`${profile.inbound_tag}:${index}`}
                  position={index + 1}
                  profile={profile}
                />
              ) : (
                <UnavailableProfileCard
                  key={`${profile.inbound_tag ?? "unknown"}:${index}`}
                  position={index + 1}
                  profile={profile}
                />
              ),
            )}
          </div>
        ) : (
          <ProfileState title="No profile results">
            No matching profile result was returned for this User.
          </ProfileState>
        )
      ) : profiles.state === "gone_user" ? (
        <ProfileState title="No connection profiles">
          Gone Users keep Traffic and presence history, but xform no longer has current credentials
          to expose.
        </ProfileState>
      ) : profiles.state === "no_matching_inbound" ? (
        <ProfileState title="No matching inbound">
          This User is not present in a current matching VLESS inbound.
        </ProfileState>
      ) : (
        <ProfileState title="Profile source unavailable" error>
          The xray config has never parsed successfully, so matching inbounds cannot be identified.
        </ProfileState>
      )}
    </section>
  );
}

// AvailableProfileCard is the approved fully expanded card: everything a
// client needs, shown at once. Nothing here is masked — the dialog exists to
// hand over a credential (map #18).
function AvailableProfileCard({
  profile,
  position,
}: {
  profile: AvailableConnectionProfile;
  position: number;
}) {
  return (
    <article
      aria-label={`${profile.name} Connection profile`}
      className="border-border-strong bg-card/70 overflow-hidden rounded-xl border"
    >
      <header className="border-border flex flex-wrap items-center gap-2.5 border-b px-4 py-3">
        <ProfilePosition position={position} />
        <h4 className="text-[13px] font-semibold">{profile.name}</h4>
        <code className="text-muted-foreground text-[10px]">{profile.inbound_tag}</code>
      </header>
      {/* The QR moves beside the fields once there is room for it; the fields
          stay in one column until two of them can hold a host and port
          without breaking mid-value. */}
      <div className="xs:grid-cols-[minmax(0,1fr)_144px] grid gap-4 p-4">
        <div className="grid gap-2 md:grid-cols-2">
          <ProfileField
            className="md:col-span-2"
            label="Client ID"
            action={
              <CopyButton label={`Copy Client ID for ${profile.name}`} value={profile.client_id} />
            }
          >
            {profile.client_id}
          </ProfileField>
          <ProfileField label="Flow">
            {/* An absent flow is a fact about the profile, not a gap. */}
            {profile.flow ?? <span className="text-muted-foreground">none</span>}
          </ProfileField>
          <ProfileField label="Public endpoint">
            {profile.endpoint.host}:{profile.endpoint.port}
          </ProfileField>
          <ProfileField label="Transport">
            {profile.transport.type}
            <TypedValues values={transportValues(profile.transport)} />
          </ProfileField>
          <ProfileField label="Security">
            {profile.security.type}
            <TypedValues values={securityValues(profile.security)} />
          </ProfileField>
        </div>
        {/* The QR sits on its own light ground: scanners need the contrast,
            and the symbol carries its quiet zone in the drawn viewBox. */}
        <div className="bg-qr-paper xs:justify-self-stretch self-start justify-self-center rounded-[10px] p-2.5 text-center">
          <QrCode
            className="mx-auto size-[124px]"
            label={`Connection QR code for ${profile.name}`}
            value={profile.uri}
          />
          <small className="text-qr-caption mt-1.5 block text-[9px]">Scan to connect</small>
        </div>
      </div>
      <div className="border-border mx-4 mb-4 rounded-[9px] border px-3 py-2.5">
        <FieldLabel
          action={
            <CopyButton label={`Copy connection URI for ${profile.name}`} value={profile.uri} />
          }
        >
          Connection URI
        </FieldLabel>
        {/* The one string display, copy, and QR all read from. */}
        <code className="mt-1.5 block max-h-[62px] overflow-auto [overflow-wrap:anywhere] text-[10.5px] leading-[1.5]">
          {profile.uri}
        </code>
      </div>
    </article>
  );
}

// UnavailableProfileCard names the inbound that failed and why. It carries no
// Client ID action, no partial URI, and no QR: a half-built profile would be
// worse than none (IN-DEV-SPEC §7.3).
function UnavailableProfileCard({
  profile,
  position,
}: {
  profile: UnavailableConnectionProfile;
  position: number;
}) {
  return (
    <article
      aria-label={`${profile.name ?? "Unknown"} unavailable Connection profile`}
      className="border-destructive/30 bg-destructive/10 rounded-xl border px-4 py-3"
    >
      <div className="flex flex-wrap items-center gap-2.5">
        <ProfilePosition position={position} />
        <h4 className="text-[13px] font-semibold">{profile.name ?? "Unknown profile"}</h4>
        {profile.inbound_tag !== null ? (
          <code className="text-muted-foreground text-[10px]">{profile.inbound_tag}</code>
        ) : null}
      </div>
      <p className="text-destructive-foreground mt-2 text-xs">
        <code>{profile.reason}</code>: {profile.message}
      </p>
    </article>
  );
}

// ProfilePosition marks a card's place in xray inbound order, as the approved
// prototype numbers them — the order is the only thing tying a card to an
// inbound when several look alike.
function ProfilePosition({ position }: { position: number }) {
  return (
    <span
      aria-hidden="true"
      className="bg-accent text-primary grid size-[21px] place-items-center rounded-md font-mono text-[11px] font-bold"
    >
      {position}
    </span>
  );
}

function ProfileField({
  label,
  action,
  className,
  children,
}: {
  label: string;
  action?: ReactNode;
  className?: string;
  children: ReactNode;
}) {
  return (
    <div
      className={cn(
        "border-border bg-background/50 min-w-0 rounded-[9px] border px-3 py-2.5",
        className,
      )}
    >
      <FieldLabel action={action}>{label}</FieldLabel>
      <code className="mt-1.5 block [overflow-wrap:anywhere] text-[11px] leading-[1.45]">
        {children}
      </code>
    </div>
  );
}

function FieldLabel({ action, children }: { action?: ReactNode; children: ReactNode }) {
  return (
    <div className="text-muted-foreground flex items-center justify-between gap-2 text-[9px] font-extrabold tracking-[0.08em] uppercase">
      <span>{children}</span>
      {action}
    </div>
  );
}

// TypedValues prints the values the URI was built from, so an admin can read
// what a client will actually do without parsing the query string.
function TypedValues({ values }: { values: TypedValue[] }) {
  if (values.length === 0) return null;
  return (
    <span className="text-muted-foreground mt-1 block">
      {values.map(([label, value]) => (
        <span key={label} className="mr-2.5 inline-block">
          {label}{" "}
          {value === "" ? (
            // An explicitly empty REALITY short ID is a real setting.
            <span className="opacity-70">empty</span>
          ) : (
            <span className="text-foreground/80">{value}</span>
          )}
        </span>
      ))}
    </span>
  );
}

function ProfileState({
  title,
  error = false,
  children,
}: {
  title: string;
  error?: boolean;
  children: ReactNode;
}) {
  return (
    <div
      className={cn(
        "rounded-[10px] border border-dashed px-4 py-4 text-center text-sm",
        error ? "border-destructive/40 text-destructive-foreground" : "border-border-strong text-muted-foreground",
      )}
    >
      <strong className="text-foreground block">{title}</strong>
      <span>{children}</span>
    </div>
  );
}
