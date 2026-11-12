import type { FormEvent, ReactNode } from "react";
import { Link } from "@tanstack/react-router";
import type {
  AdminGroup,
  AdminSnapshot,
  AdminUser,
  ConnectionDetail,
  ConnectionFormData,
  ConnectionRow,
  FormAction,
  ViewStatus,
} from "./types";
import {
  wsAlert,
  wsButton,
  wsChip,
  wsChipAmber,
  wsChipGreen,
  wsChipTeal,
  wsEmpty,
  wsFocus,
  wsHeading,
  wsInput,
  wsLabel,
  wsLink,
  wsMuted,
  wsNotice,
  wsPage,
  wsPanel,
  wsPrimary,
  wsSelect,
  wsSubhead,
  wsSuccess,
  wsTable,
  wsTd,
  wsTh,
} from "./ui";

const path = (to: string) => to as never;
const stopSubmit = (action: FormAction) => (event: FormEvent<HTMLFormElement>) => {
  event.preventDefault();
  const form = event.currentTarget;
  const data = new FormData(form);
  const password = form.querySelector<HTMLInputElement>('input[type="password"]');
  if (password) password.value = "";
  void action(data);
};

export type AdminTitle =
  | "Administration"
  | "Users"
  | "Groups"
  | "Group members"
  | "Group database access"
  | "Connections"
  | "Edit connection";

export function AdminShell({
  title,
  connectionsAvailable,
  children,
}: {
  title: AdminTitle;
  connectionsAvailable: boolean;
  children: ReactNode;
}) {
  const activeGroups =
    title === "Groups" || title === "Group members" || title === "Group database access";
  const activeConnections = title === "Connections" || title === "Edit connection";
  const tab = (label: string, href: string, active: boolean) => (
    <Link
      key={label}
      to={path(href)}
      aria-current={active ? "page" : undefined}
      className={`${active ? "-mb-px inline-flex min-h-14 items-center border-b-2 border-cyan-700 px-3 text-sm font-semibold text-cyan-900" : "-mb-px inline-flex min-h-14 items-center border-b-2 border-transparent px-3 text-sm text-slate-600 hover:border-slate-300 hover:text-slate-900"} ${wsFocus}`}
    >
      {label}
    </Link>
  );
  return (
    <div className={`${wsPage} flex min-h-screen flex-col`}>
      <header className="border-b border-slate-200 bg-white">
        <div className="mx-auto flex w-full max-w-7xl flex-wrap items-center gap-x-6 px-4 sm:px-6">
          <Link
            to={path("/admin")}
            className={`inline-flex min-h-14 items-center gap-2 text-base font-bold tracking-tight text-cyan-900 ${wsFocus}`}
          >
            <svg
              width="22"
              height="22"
              viewBox="0 0 24 24"
              fill="none"
              stroke="currentColor"
              strokeWidth="1.75"
              strokeLinecap="round"
              aria-hidden="true"
            >
              <ellipse cx="12" cy="6" rx="7" ry="3" />
              <path d="M5 6v6c0 1.7 3.1 3 7 3s7-1.3 7-3V6" />
              <path d="M5 12v6c0 1.7 3.1 3 7 3s7-1.3 7-3v-6" />
            </svg>
            sqlwise <span className={wsChip}>Admin</span>
          </Link>
          <nav
            aria-label="Administration"
            className="flex min-w-0 basis-full items-stretch gap-x-1 self-stretch overflow-x-auto overflow-y-hidden whitespace-nowrap text-sm sm:basis-0 sm:flex-1"
          >
            {tab("Overview", "/admin", title === "Administration")}
            {tab("Users", "/admin/users", title === "Users")}
            {tab("Groups", "/admin/groups", activeGroups)}
            {connectionsAvailable ? (
              tab("Connections", "/admin/databases", activeConnections)
            ) : (
              <span
                className="-mb-px inline-flex min-h-14 cursor-not-allowed items-center border-b-2 border-transparent px-3 text-slate-600"
                aria-label="Connection management unavailable"
              >
                Connections · unavailable
              </span>
            )}
            <Link
              to={path("/account")}
              className={`ml-auto inline-flex min-h-11 items-center self-center rounded-lg px-3 text-slate-700 hover:bg-slate-50 hover:text-cyan-800 ${wsFocus}`}
            >
              Account
            </Link>
          </nav>
        </div>
      </header>
      <div className="mx-auto w-full max-w-7xl flex-1 px-4 py-8 sm:px-6">{children}</div>
    </div>
  );
}

function Feedback({ status = {} }: { status?: ViewStatus }) {
  return (
    <>
      {status.message && (
        <p role="status" aria-live="polite" className={`${wsNotice} mt-6 break-words`}>
          {status.message}
        </p>
      )}
      {status.error && (
        <p role="alert" className={`${wsAlert} mt-6 break-words`}>
          {status.error}
          {status.refreshNeeded && status.onRefresh && (
            <button
              type="button"
              className={`${wsButton} ml-3`}
              onClick={() => void status.onRefresh?.()}
            >
              Refresh session and data
            </button>
          )}
        </p>
      )}
    </>
  );
}
function ConnectionFeedback({ status = {} }: { status?: ViewStatus }) {
  return (
    <>
      {status.message && (
        <p role="status" aria-live="polite" className={`${wsNotice} mb-5`}>
          {status.message}
        </p>
      )}
      {status.error && (
        <p role="alert" className={`${wsAlert} mb-5`}>
          {status.error}
          {status.refreshNeeded && status.onRefresh && (
            <button
              type="button"
              className={`${wsButton} ml-3`}
              onClick={() => void status.onRefresh?.()}
            >
              Refresh session and data
            </button>
          )}
        </p>
      )}
    </>
  );
}
function Submit({
  children,
  primary = false,
  className = "",
  pending = false,
}: {
  children: ReactNode;
  primary?: boolean;
  className?: string;
  pending?: boolean;
}) {
  return (
    <button
      type="submit"
      disabled={pending}
      aria-busy={pending || undefined}
      className={`${primary ? wsPrimary : wsButton} ${className}`}
    >
      {children}
    </button>
  );
}
function Card({
  title,
  description,
  href,
  action,
}: {
  title: string;
  description: string;
  href: string;
  action: string;
}) {
  return (
    <section className={`${wsPanel} flex flex-col p-5`}>
      <h2 className={wsSubhead}>{title}</h2>
      <p className={`${wsMuted} mt-1.5 flex-1 leading-6`}>{description}</p>
      <Link to={path(href)} className={`${wsButton} mt-4 self-start`}>
        {action}
      </Link>
    </section>
  );
}

export function DashboardView({
  data,
  status,
  connectionsAvailable,
}: {
  data: AdminSnapshot;
  status?: ViewStatus;
  connectionsAvailable: boolean;
}) {
  return (
    <AdminShell title="Administration" connectionsAvailable={connectionsAvailable}>
      <main>
        <h1 className={wsHeading}>Platform administration</h1>
        <p className={`${wsMuted} mt-2 max-w-2xl leading-6`}>
          Manage platform accounts and group access. Platform administrator status does not grant
          access to target databases.
        </p>
        <Feedback status={status} />
        <div className="mt-6 grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          <Card
            title="Users"
            description="Create accounts, update usernames and disable access."
            href="/admin/users"
            action="Manage users"
          />
          <Card
            title="Groups"
            description="Organize memberships and target permissions."
            href="/admin/groups"
            action="Manage groups"
          />
          {connectionsAvailable ? (
            <Card
              title="Connections"
              description="Register target databases for access grants."
              href="/admin/databases"
              action="Manage connections"
            />
          ) : (
            <section className={`${wsPanel} flex flex-col p-5`}>
              <h2 className={wsSubhead}>Connections</h2>
              <p className={`${wsMuted} mt-1.5 flex-1 leading-6`}>
                Registered database choices appear here when connection management is integrated.
              </p>
              <p role="status" className={`${wsChip} mt-4 self-start`}>
                Connection management unavailable
              </p>
            </section>
          )}
        </div>
        <section className={`${wsPanel} mt-6 p-5`} aria-labelledby="registered-targets">
          <h2 id="registered-targets" className={wsSubhead}>
            Registered databases
          </h2>
          {data.databases.length === 0 && (
            <p className={`${wsMuted} mt-2`}>No databases are registered yet.</p>
          )}
          <ul className="mt-3 divide-y divide-slate-100 empty:hidden">
            {data.databases.map((db) => (
              <li
                key={db.id}
                className="flex flex-wrap items-center justify-between gap-3 py-3 text-sm"
              >
                <span className="break-all font-medium text-slate-900">{db.name}</span>
                <span className={db.active ? wsChipGreen : wsChip}>
                  {db.active ? "Active" : "Inactive"}
                </span>
              </li>
            ))}
          </ul>
        </section>
      </main>
    </AdminShell>
  );
}

export interface UsersViewProps extends ViewStatus {
  data: AdminSnapshot;
  connectionsAvailable: boolean;
  onCreate: FormAction;
  onRename: (id: string, data: FormData) => void | Promise<void>;
  onPassword: (id: string, data: FormData) => void | Promise<void>;
  onAdmin: (id: string, admin: boolean) => void | Promise<void>;
  onActive: (id: string, active: boolean) => void | Promise<void>;
}
export function UsersView({
  data,
  connectionsAvailable,
  onCreate,
  onRename,
  onPassword,
  onAdmin,
  onActive,
  message,
  error,
  pending = false,
  refreshNeeded,
  onRefresh,
}: UsersViewProps) {
  return (
    <AdminShell title="Users" connectionsAvailable={connectionsAvailable}>
      <main>
        <h1 className={wsHeading}>Users</h1>
        <p className={`${wsMuted} mt-2 leading-6`}>
          Create accounts and disable access while preserving account history.
        </p>
        <Feedback status={{ message, error, refreshNeeded, onRefresh }} />
        <div className="mt-6 flex flex-wrap items-start gap-6">
          <section className={`${wsPanel} min-w-0 flex-[1_1_18rem] p-5`}>
            <h2 className={wsSubhead}>Create user</h2>
            <form onSubmit={stopSubmit(onCreate)} className="mt-4 space-y-4">
              <div className="space-y-1.5">
                <label htmlFor="create-username" className={wsLabel}>
                  Username
                </label>
                <input
                  id="create-username"
                  name="username"
                  type="text"
                  autoComplete="username"
                  autoCapitalize="none"
                  spellCheck={false}
                  required
                  maxLength={64}
                  className={wsInput}
                />
              </div>
              <div className="space-y-1.5">
                <label htmlFor="create-password" className={wsLabel}>
                  Temporary password
                </label>
                <input
                  id="create-password"
                  name="password"
                  type="password"
                  autoComplete="new-password"
                  required
                  maxLength={128}
                  className={wsInput}
                />
              </div>
              <Submit primary className="w-full" pending={pending}>
                Create user
              </Submit>
            </form>
          </section>
          <section className={`${wsPanel} min-w-0 flex-[999_1_36rem] p-5`}>
            <h2 className={wsSubhead}>User accounts</h2>
            {data.users.length === 0 && <p className={`${wsMuted} mt-2`}>No users yet.</p>}
            <ul className="mt-2 divide-y divide-slate-100 empty:hidden">
              {data.users.map((user) => (
                <UserRow
                  key={user.id}
                  user={user}
                  pending={pending}
                  onRename={onRename}
                  onPassword={onPassword}
                  onAdmin={onAdmin}
                  onActive={onActive}
                />
              ))}
            </ul>
          </section>
        </div>
      </main>
    </AdminShell>
  );
}
function UserRow({
  user,
  pending,
  onRename,
  onPassword,
  onAdmin,
  onActive,
}: Pick<UsersViewProps, "onRename" | "onPassword" | "onAdmin" | "onActive"> & {
  user: AdminUser;
  pending: boolean;
}) {
  return (
    <li className="min-w-0 py-5">
      <div className="flex flex-wrap items-center gap-2">
        <span className="mr-auto break-all text-sm font-semibold text-slate-900">
          {user.username}
        </span>
        <span className={user.is_platform_admin ? wsChipTeal : wsChip}>
          {user.is_platform_admin ? "Platform admin" : "Regular user"}
        </span>
        <span className={user.active ? wsChipGreen : wsChip}>
          {user.active ? "Active" : "Disabled"}
        </span>
      </div>
      <div className="mt-3 flex flex-col gap-3">
        <form
          onSubmit={stopSubmit((data) => onRename(user.id, data))}
          className="flex min-w-0 flex-wrap gap-2"
        >
          <label className="sr-only" htmlFor={`username-${user.id}`}>
            Update username for {user.username}
          </label>
          <input
            id={`username-${user.id}`}
            name="username"
            type="text"
            defaultValue={user.username}
            required
            maxLength={64}
            className={`${wsInput} min-w-0 flex-1`}
          />
          <Submit pending={pending}>Save name</Submit>
        </form>
        <form onSubmit={stopSubmit((data) => onPassword(user.id, data))} className="min-w-0">
          <div className="flex min-w-0 flex-wrap items-end gap-2">
            <div className="min-w-0 flex-1 space-y-1.5">
              <label className={wsLabel} htmlFor={`password-${user.id}`}>
                Set new password for {user.username}
              </label>
              <input
                id={`password-${user.id}`}
                name="password"
                type="password"
                autoComplete="new-password"
                required
                minLength={12}
                maxLength={128}
                aria-describedby={`password-help-${user.id}`}
                className={wsInput}
              />
            </div>
            <Submit pending={pending}>Reset password</Submit>
          </div>
          <p id={`password-help-${user.id}`} className={`${wsMuted} mt-1.5`}>
            Use at least 12 characters. The password is never shown after submission.
          </p>
        </form>
        <div className="flex flex-wrap gap-2">
          <form
            onSubmit={(e) => {
              e.preventDefault();
              void onAdmin(user.id, !user.is_platform_admin);
            }}
          >
            <Submit pending={pending}>
              <span className={user.is_platform_admin ? "text-rose-800" : ""}>
                {user.is_platform_admin ? "Remove admin" : "Make admin"}
              </span>
            </Submit>
          </form>
          <form
            onSubmit={(e) => {
              e.preventDefault();
              void onActive(user.id, !user.active);
            }}
          >
            <Submit pending={pending}>
              <span className={user.active ? "text-rose-800" : ""}>
                {user.active ? "Disable" : "Re-enable"}
              </span>
            </Submit>
          </form>
        </div>
      </div>
    </li>
  );
}

export interface GroupsViewProps extends ViewStatus {
  data: AdminSnapshot;
  connectionsAvailable: boolean;
  onCreate: FormAction;
  onRename: (id: string, data: FormData) => void | Promise<void>;
  onActive: (id: string, active: boolean) => void | Promise<void>;
}
export function GroupsView({
  data,
  connectionsAvailable,
  onCreate,
  onRename,
  onActive,
  message,
  error,
  pending = false,
  refreshNeeded,
  onRefresh,
}: GroupsViewProps) {
  return (
    <AdminShell title="Groups" connectionsAvailable={connectionsAvailable}>
      <main>
        <h1 className={wsHeading}>Groups</h1>
        <p className={`${wsMuted} mt-2 leading-6`}>
          Membership and database grants are managed per group.
        </p>
        <Feedback status={{ message, error, refreshNeeded, onRefresh }} />
        <div className="mt-6 flex flex-wrap items-start gap-6">
          <section className={`${wsPanel} min-w-0 flex-[1_1_18rem] p-5`}>
            <h2 className={wsSubhead}>Create group</h2>
            <form onSubmit={stopSubmit(onCreate)} className="mt-4 space-y-4">
              <div className="space-y-1.5">
                <label htmlFor="create-group-name" className={wsLabel}>
                  Group name
                </label>
                <input
                  id="create-group-name"
                  name="name"
                  type="text"
                  required
                  maxLength={64}
                  className={wsInput}
                />
              </div>
              <Submit primary className="w-full" pending={pending}>
                Create group
              </Submit>
            </form>
          </section>
          <section className={`${wsPanel} min-w-0 flex-[999_1_36rem] p-5`}>
            <h2 className={wsSubhead}>Groups</h2>
            {data.groups.length === 0 && <p className={`${wsMuted} mt-2`}>No groups yet.</p>}
            <ul className="mt-2 divide-y divide-slate-100 empty:hidden">
              {data.groups.map((group) => (
                <GroupRow
                  key={group.id}
                  group={group}
                  pending={pending}
                  onRename={onRename}
                  onActive={onActive}
                />
              ))}
            </ul>
          </section>
        </div>
      </main>
    </AdminShell>
  );
}
function GroupRow({
  group,
  pending,
  onRename,
  onActive,
}: Pick<GroupsViewProps, "onRename" | "onActive"> & { group: AdminGroup; pending: boolean }) {
  return (
    <li className="flex flex-wrap items-center justify-between gap-2 py-4">
      <form
        onSubmit={stopSubmit((data) => onRename(group.id, data))}
        className="flex min-w-0 flex-[1_1_16rem] flex-wrap gap-2"
      >
        <label className="sr-only" htmlFor={`group-name-${group.id}`}>
          Update group name for {group.name}
        </label>
        <input
          id={`group-name-${group.id}`}
          name="name"
          type="text"
          defaultValue={group.name}
          required
          maxLength={64}
          className={`${wsInput} min-w-0 flex-1`}
        />
        <Submit pending={pending}>Save</Submit>
      </form>
      <Link
        className={`${wsLink} inline-flex min-h-11 items-center px-2 text-sm`}
        to={path(`/admin/groups/${group.id}/members`)}
      >
        Members and databases
      </Link>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          void onActive(group.id, !group.active);
        }}
      >
        <Submit pending={pending}>
          <span className={group.active ? "text-rose-800" : ""}>
            {group.active ? "Disable" : "Re-enable"}
          </span>
        </Submit>
      </form>
    </li>
  );
}

export interface MembersViewProps extends ViewStatus {
  data: AdminSnapshot;
  connectionsAvailable: boolean;
  onAdd: FormAction;
  onRemove: (userId: string) => void | Promise<void>;
  onLeader: FormAction;
}
export function MembersView({
  data,
  connectionsAvailable,
  onAdd,
  onRemove,
  onLeader,
  message,
  error,
  pending = false,
  refreshNeeded,
  onRefresh,
}: MembersViewProps) {
  const group = data.selected_group;
  if (!group)
    return (
      <AdminShell title="Group members" connectionsAvailable={connectionsAvailable}>
        <p role="alert" className={wsAlert}>
          Group not found.
        </p>
      </AdminShell>
    );
  const memberships = data.memberships.filter((row) => row.group_id === group.id);
  const leader = data.users.find((user) => user.id === group.leader_user_id);
  return (
    <AdminShell title="Group members" connectionsAvailable={connectionsAvailable}>
      <main>
        <Link
          className={`${wsLink} inline-flex min-h-11 items-center text-sm`}
          to={path("/admin/groups")}
        >
          Groups
        </Link>
        <h1 className={`${wsHeading} mt-1 break-words`}>Members of {group.name}</h1>
        <p className={`${wsMuted} mt-2 leading-6`}>
          Adding or removing membership changes effective database access.
        </p>
        <Feedback status={{ message, error, refreshNeeded, onRefresh }} />
        <section className={`${wsPanel} mt-6 p-5`}>
          <h2 className={wsSubhead}>Group leader</h2>
          <p className={`${wsMuted} mt-2`}>
            {leader
              ? `${leader.username}${leader.active ? "" : " (inactive; approvals are blocked until an active member is assigned)"}`
              : "No leader assigned. Approvals for this group are blocked until an active member is assigned."}
          </p>
          <form onSubmit={stopSubmit(onLeader)} className="mt-4 flex flex-wrap items-end gap-3">
            <div className="min-w-0 flex-[1_1_12rem] space-y-1.5">
              <label htmlFor="group-leader" className={wsLabel}>
                Leader
              </label>
              <select
                id="group-leader"
                name="user_id"
                className={`${wsSelect} w-full`}
                defaultValue={group.leader_user_id}
                key={group.leader_user_id}
              >
                <option value="">No leader</option>
                {leader && !leader.active && (
                  <option value={leader.id} disabled>
                    {leader.username} (inactive)
                  </option>
                )}
                {memberships
                  .filter((member) =>
                    data.users.some((user) => user.id === member.user_id && user.active),
                  )
                  .map((member) => (
                    <option key={member.user_id} value={member.user_id}>
                      {member.username}
                    </option>
                  ))}
              </select>
            </div>
            <Submit primary pending={pending}>
              Save leader
            </Submit>
          </form>
        </section>
        <div className="mt-6 flex flex-wrap items-start gap-6">
          <section className={`${wsPanel} min-w-0 flex-[1_1_18rem] p-5`}>
            <h2 className={wsSubhead}>Add member</h2>
            <form onSubmit={stopSubmit(onAdd)} className="mt-4 flex flex-wrap items-end gap-3">
              <div className="min-w-0 flex-[1_1_12rem] space-y-1.5">
                <label htmlFor="member-user" className={wsLabel}>
                  User
                </label>
                <select
                  id="member-user"
                  name="user_id"
                  required
                  className={`${wsSelect} w-full`}
                  defaultValue=""
                >
                  <option value="">Choose user</option>
                  {data.users
                    .filter((user) => user.active)
                    .map((user) => (
                      <option key={user.id} value={user.id}>
                        {user.username}
                      </option>
                    ))}
                </select>
              </div>
              <Submit primary pending={pending}>
                Add member
              </Submit>
            </form>
          </section>
          <section className={`${wsPanel} min-w-0 flex-[999_1_32rem] p-5`}>
            <h2 className={wsSubhead}>Current members</h2>
            {memberships.length === 0 && (
              <p className={`${wsMuted} mt-2`}>No members in this group.</p>
            )}
            <ul className="mt-2 divide-y divide-slate-100 empty:hidden">
              {memberships.map((member) => (
                <li
                  key={member.user_id}
                  className="flex flex-wrap items-center justify-between gap-3 py-3"
                >
                  <span className="break-all text-sm font-medium text-slate-900">
                    {member.username}
                  </span>
                  <form
                    onSubmit={(e) => {
                      e.preventDefault();
                      void onRemove(member.user_id);
                    }}
                  >
                    <Submit pending={pending}>
                      <span className="text-rose-800">Remove member</span>
                    </Submit>
                  </form>
                </li>
              ))}
            </ul>
          </section>
        </div>
      </main>
    </AdminShell>
  );
}

export interface GrantsViewProps extends ViewStatus {
  data: AdminSnapshot;
  connectionsAvailable: boolean;
  onSave: FormAction;
  onRemove: (databaseId: string) => void | Promise<void>;
}
export function GrantsView({
  data,
  connectionsAvailable,
  onSave,
  onRemove,
  message,
  error,
  pending = false,
  refreshNeeded,
  onRefresh,
}: GrantsViewProps) {
  const group = data.selected_group;
  if (!group)
    return (
      <AdminShell title="Group database access" connectionsAvailable={connectionsAvailable}>
        <p role="alert" className={wsAlert}>
          Group not found.
        </p>
      </AdminShell>
    );
  const grants = data.grants.filter((row) => row.group_id === group.id);
  const activeDatabases = data.databases.filter((db) => db.active);
  return (
    <AdminShell title="Group database access" connectionsAvailable={connectionsAvailable}>
      <main>
        <Link
          className={`${wsLink} inline-flex min-h-11 items-center text-sm`}
          to={path(`/admin/groups/${group.id}/members`)}
        >
          Group members
        </Link>
        <h1 className={`${wsHeading} mt-1 break-words`}>Database access for {group.name}</h1>
        <p className={`${wsMuted} mt-2 leading-6`}>
          Grant view or edit access to active registered databases.
        </p>
        <Feedback status={{ message, error, refreshNeeded, onRefresh }} />
        <div className="mt-6 flex flex-wrap items-start gap-6">
          <section className={`${wsPanel} min-w-0 flex-[1_1_20rem] p-5`}>
            <h2 className={wsSubhead}>Set access</h2>
            <form onSubmit={stopSubmit(onSave)} className="mt-4 space-y-4">
              <div className="space-y-1.5">
                <label htmlFor="grant-db" className={wsLabel}>
                  Registered database
                </label>
                <select
                  id="grant-db"
                  name="database_id"
                  required
                  className={`${wsSelect} w-full`}
                  defaultValue=""
                >
                  <option value="">Choose database</option>
                  {activeDatabases.map((db) => (
                    <option key={db.id} value={db.id}>
                      {db.name}
                    </option>
                  ))}
                </select>
              </div>
              <div className="space-y-1.5">
                <label htmlFor="grant-permission" className={wsLabel}>
                  Permission
                </label>
                <select
                  id="grant-permission"
                  name="permission"
                  required
                  className={`${wsSelect} w-full`}
                  defaultValue="view"
                >
                  <option value="view">View</option>
                  <option value="edit">Edit</option>
                </select>
              </div>
              <Submit primary className="w-full" pending={pending}>
                Save access
              </Submit>
            </form>
            {activeDatabases.length === 0 && (
              <p className={`${wsMuted} mt-4 leading-6`}>
                No active registered databases are available.{" "}
                {connectionsAvailable ? (
                  <>
                    Register one through{" "}
                    <Link to={path("/admin/databases")} className={wsLink}>
                      Connections
                    </Link>
                    .
                  </>
                ) : (
                  "Connection management is unavailable until its integration is ready."
                )}
              </p>
            )}
          </section>
          <section className={`${wsPanel} min-w-0 flex-[999_1_32rem] p-5`}>
            <h2 className={wsSubhead}>Current grants</h2>
            {grants.length === 0 && (
              <p className={`${wsMuted} mt-2`}>No database grants for this group.</p>
            )}
            <ul className="mt-2 divide-y divide-slate-100 empty:hidden">
              {grants.map((grant) => (
                <li
                  key={grant.database_id}
                  className="flex flex-wrap items-center justify-between gap-3 py-3"
                >
                  <span className="flex min-w-0 flex-wrap items-center gap-2 text-sm">
                    <span className="break-words font-medium text-slate-900">
                      {grant.database_name}
                    </span>
                    <span
                      className={grant.permission.toLowerCase() === "edit" ? wsChipTeal : wsChip}
                    >
                      {grant.permission}
                    </span>
                  </span>
                  <form
                    onSubmit={(e) => {
                      e.preventDefault();
                      void onRemove(grant.database_id);
                    }}
                  >
                    <Submit pending={pending}>
                      <span className="text-rose-800">Remove grant</span>
                    </Submit>
                  </form>
                </li>
              ))}
            </ul>
          </section>
        </div>
      </main>
    </AdminShell>
  );
}

export interface ConnectionsViewProps extends ViewStatus {
  connections: ConnectionRow[];
  initialForm?: Partial<ConnectionFormData>;
  connectionsAvailable?: boolean;
  onCreate: FormAction;
}
export function ConnectionsView({
  connections,
  initialForm = {},
  connectionsAvailable = true,
  onCreate,
  message,
  error,
  pending = false,
  refreshNeeded,
  onRefresh,
}: ConnectionsViewProps) {
  return (
    <AdminShell title="Connections" connectionsAvailable={connectionsAvailable}>
      <main>
        <h1 className={wsHeading}>Connections</h1>
        <p className={`${wsMuted} mt-2 max-w-2xl leading-6`}>
          Register target databases. Passwords are stored encrypted and never shown again. Grant
          access to a connection through a group.
        </p>
        <div className="mt-6">
          <ConnectionFeedback status={{ message, error, refreshNeeded, onRefresh }} />
          <div className="flex flex-wrap items-start gap-6">
            <section
              className="min-w-0 flex-[999_1_34rem]"
              aria-labelledby="registered-connections"
            >
              <h2 id="registered-connections" className={wsSubhead}>
                Registered connections
              </h2>
              {connections.length === 0 ? (
                <p className={`${wsEmpty} mt-3`}>No connections are registered yet.</p>
              ) : (
                <div className={`${wsPanel} mt-3 overflow-x-auto`}>
                  <table className={`${wsTable} min-w-[36rem]`}>
                    <thead>
                      <tr>
                        <th scope="col" className={wsTh}>
                          Name
                        </th>
                        <th scope="col" className={wsTh}>
                          Engine
                        </th>
                        <th scope="col" className={wsTh}>
                          Address
                        </th>
                        <th scope="col" className={wsTh}>
                          TLS
                        </th>
                        <th scope="col" className={wsTh}>
                          Status
                        </th>
                      </tr>
                    </thead>
                    <tbody>
                      {connections.map((row) => (
                        <tr key={row.id} className="hover:bg-slate-50">
                          <td className={`${wsTd} py-1.5`}>
                            <Link
                              to={path(`/admin/databases/${row.id}`)}
                              className={`${wsLink} inline-flex min-h-11 items-center break-all font-medium`}
                            >
                              {row.name}
                            </Link>
                          </td>
                          <td className={`${wsTd} py-3.5`}>
                            <span className={wsChip}>{row.dbms}</span>
                          </td>
                          <td
                            className={`${wsTd} break-all py-3.5 font-mono text-[13px] text-slate-800`}
                          >
                            {row.username}@{row.address}
                          </td>
                          <td
                            className={`${wsTd} whitespace-nowrap py-3.5 font-mono text-[13px] text-slate-700`}
                          >
                            {row.tls}
                          </td>
                          <td className={`${wsTd} py-3.5`}>
                            <span className={row.active ? wsChipGreen : wsChip}>
                              {row.active ? "Active" : "Disabled"}
                            </span>
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </section>
            <section
              className={`${wsPanel} min-w-0 flex-[1_1_20rem] p-5`}
              aria-labelledby="create-connection"
            >
              <h2 id="create-connection" className={wsSubhead}>
                Register connection
              </h2>
              <form onSubmit={stopSubmit(onCreate)} className="mt-4 space-y-4">
                <ConnectionFields
                  form={{ ...emptyConnectionForm, ...initialForm, existing: false }}
                />
                <Submit primary className="w-full" pending={pending}>
                  Register connection
                </Submit>
              </form>
            </section>
          </div>
        </div>
      </main>
    </AdminShell>
  );
}

const emptyConnectionForm: ConnectionFormData = {
  name: "",
  dbms: "postgresql",
  host: "",
  port: "5432",
  database: "",
  username: "",
  tls: "verify-full",
  active: true,
  existing: false,
  sandbox: false,
};
function ConnectionFields({ form }: { form: ConnectionFormData }) {
  return (
    <>
      <div className="space-y-1.5">
        <label htmlFor="connection-name" className={wsLabel}>
          Display name
        </label>
        <input
          id="connection-name"
          name="name"
          defaultValue={form.name}
          required
          maxLength={128}
          className={wsInput}
        />
      </div>
      <div className="space-y-1.5">
        <label htmlFor="connection-dbms" className={wsLabel}>
          Database type
        </label>
        <select
          id="connection-dbms"
          name="dbms"
          required
          defaultValue={form.dbms || "postgresql"}
          className={`${wsSelect} w-full`}
        >
          <option value="postgresql">PostgreSQL</option>
          <option value="mysql">MySQL (bonus; migrations are not atomic)</option>
        </select>
      </div>
      <div className="grid gap-4 sm:grid-cols-[minmax(0,1fr)_7rem]">
        <div className="space-y-1.5">
          <label htmlFor="connection-host" className={wsLabel}>
            Host
          </label>
          <input
            id="connection-host"
            name="host"
            defaultValue={form.host}
            required
            autoComplete="off"
            className={`${wsInput} font-mono`}
          />
        </div>
        <div className="space-y-1.5">
          <label htmlFor="connection-port" className={wsLabel}>
            Port
          </label>
          <input
            id="connection-port"
            name="port"
            defaultValue={form.port}
            required
            inputMode="numeric"
            className={`${wsInput} font-mono`}
          />
        </div>
      </div>
      <div className="space-y-1.5">
        <label htmlFor="connection-database" className={wsLabel}>
          Database name
        </label>
        <input
          id="connection-database"
          name="database"
          defaultValue={form.database}
          required
          autoComplete="off"
          className={`${wsInput} font-mono`}
        />
      </div>
      <div className="space-y-1.5">
        <label htmlFor="connection-username" className={wsLabel}>
          Username
        </label>
        <input
          id="connection-username"
          name="username"
          defaultValue={form.username}
          required
          autoComplete="off"
          className={`${wsInput} font-mono`}
        />
      </div>
      <div className="space-y-1.5">
        <label htmlFor="connection-password" className={wsLabel}>
          Password
        </label>
        <input
          id="connection-password"
          name="password"
          type="password"
          autoComplete="new-password"
          aria-describedby={form.existing ? "connection-password-hint" : undefined}
          className={wsInput}
        />
        {form.existing && (
          <p id="connection-password-hint" className={wsMuted}>
            Leave blank to keep the current password.
          </p>
        )}
      </div>
      <label className="flex min-h-11 cursor-pointer items-start gap-3 py-1 text-sm text-slate-900">
        <input
          type="checkbox"
          name="sandbox"
          value="true"
          defaultChecked={form.sandbox}
          className={`mt-0.5 size-4 shrink-0 accent-cyan-700 ${wsFocus}`}
        />
        <span className="font-medium">
          Analyze sandbox{" "}
          <span className="mt-0.5 block font-normal leading-6 text-slate-600">
            PostgreSQL only. Lets editors run EXPLAIN ANALYZE on SELECT statements here. Use an
            isolated database and a read-only user.
          </span>
        </span>
      </label>
      <div className="space-y-1.5">
        <label htmlFor="connection-tls" className={wsLabel}>
          TLS
        </label>
        <select
          id="connection-tls"
          name="tls"
          required
          defaultValue={form.tls || "verify-full"}
          className={`${wsSelect} w-full`}
        >
          <option value="verify-full">Verify certificate and host</option>
          <option value="require">Encrypt without verification</option>
          <option value="disable">Disabled</option>
        </select>
      </div>
    </>
  );
}

export interface ConnectionDetailViewProps extends ViewStatus {
  detail: ConnectionDetail;
  connectionsAvailable?: boolean;
  onSave: FormAction;
  onTest: () => void | Promise<void>;
  onActive: (active: boolean) => void | Promise<void>;
}
export function ConnectionDetailView({
  detail,
  connectionsAvailable = true,
  onSave,
  onTest,
  onActive,
  message,
  error,
  pending = false,
  refreshNeeded,
  onRefresh,
}: ConnectionDetailViewProps) {
  const form = detail.form;
  return (
    <AdminShell title="Edit connection" connectionsAvailable={connectionsAvailable}>
      <main>
        <Link
          className={`${wsLink} inline-flex min-h-11 items-center text-sm`}
          to={path("/admin/databases")}
        >
          Connections
        </Link>
        <div className="mt-1 flex flex-wrap items-center gap-2.5">
          <h1 className={`${wsHeading} break-words`}>{form.name}</h1>
          {form.sandbox && <span className={wsChipAmber}>Analyze sandbox</span>}
        </div>
        <div className="mt-6">
          <ConnectionFeedback status={{ message, error, refreshNeeded, onRefresh }} />
          <div className="flex flex-wrap items-start gap-6">
            <section
              className={`${wsPanel} min-w-0 flex-[999_1_26rem] p-5`}
              aria-labelledby="edit-connection"
            >
              <h2 id="edit-connection" className={wsSubhead}>
                Connection settings
              </h2>
              <p className={`${wsMuted} mt-2 leading-6`}>
                A connection with migration history keeps its database type, host, port and database
                name; register a new connection for a different database.
              </p>
              <form onSubmit={stopSubmit(onSave)} className="mt-5 space-y-4">
                <ConnectionFields form={form} />
                <Submit primary className="w-full" pending={pending}>
                  Save changes
                </Submit>
              </form>
            </section>
            <div className="min-w-0 flex-[1_1_20rem] space-y-6">
              <section className={`${wsPanel} p-5`} aria-labelledby="test-connection">
                <h2 id="test-connection" className={wsSubhead}>
                  Test connection
                </h2>
                <form
                  onSubmit={(e) => {
                    e.preventDefault();
                    void onTest();
                  }}
                  className="mt-4"
                >
                  <Submit pending={pending}>Test saved connection</Submit>
                </form>
                <div className="mt-4">
                  {detail.test?.ran &&
                    (detail.test.ok ? (
                      <p role="status" className={wsSuccess}>
                        {detail.test.message}
                      </p>
                    ) : (
                      <p role="alert" className={wsAlert}>
                        {detail.test.message}
                      </p>
                    ))}
                </div>
              </section>
              <section className={`${wsPanel} p-5`} aria-labelledby="connection-status">
                <h2 id="connection-status" className={wsSubhead}>
                  Status
                </h2>
                <form
                  onSubmit={(e) => {
                    e.preventDefault();
                    void onActive(!form.active);
                  }}
                  className="mt-4 flex flex-wrap items-center justify-between gap-3"
                >
                  <p className="min-w-0 flex-[1_1_12rem] text-sm leading-6 text-slate-700">
                    {form.active
                      ? "Active. Disabling keeps grants and migration history and signs out users who can access it."
                      : "Disabled. Nobody can open this database until it is enabled."}
                  </p>
                  <Submit primary={!form.active} pending={pending}>
                    {form.active ? "Disable" : "Enable"}
                  </Submit>
                </form>
              </section>
              <section className={`${wsPanel} p-5`} aria-labelledby="connection-capabilities">
                <h2 id="connection-capabilities" className={wsSubhead}>
                  Capabilities
                </h2>
                <ul className="mt-3 divide-y divide-slate-100 text-sm">
                  {detail.capabilities.map((cap) => (
                    <li
                      key={cap.name}
                      className="flex flex-wrap items-center justify-between gap-3 py-2"
                    >
                      <span className="text-slate-800">{cap.name}</span>
                      <span className={cap.supported ? wsChipGreen : wsChip}>
                        {cap.supported ? "Supported" : "Unsupported"}
                      </span>
                    </li>
                  ))}
                </ul>
              </section>
              {detail.receipt_sql && (
                <section className={`${wsPanel} p-5`} aria-labelledby="receipt-setup">
                  <h2 id="receipt-setup" className={wsSubhead}>
                    Migration receipt setup
                  </h2>
                  <p className={`${wsMuted} mt-2 leading-6`}>
                    Run once in the target database as a role other than{" "}
                    <span className="font-mono text-slate-900">{form.username}</span>, so migrations
                    cannot change their own receipts. The user also needs the privileges its
                    migrations use.
                  </p>
                  <pre className="mt-4 overflow-x-auto rounded-lg border border-slate-200 bg-slate-50 p-4 font-mono text-xs leading-5 text-slate-900">
                    <code>{detail.receipt_sql}</code>
                  </pre>
                </section>
              )}
            </div>
          </div>
        </div>
      </main>
    </AdminShell>
  );
}
