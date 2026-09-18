import {
  Activity,
  CalendarClock,
  Cog,
  Lock,
  Network,
  Route,
  Server,
  Shield,
  Stethoscope,
} from 'lucide-vue-next'

import DashboardView from '@/views/DashboardView.vue'

/**
 * The sidebar, written once for the router, the sidebar and the tab strips.
 *
 * Three tiers, each with a test for what goes where:
 *
 * - An item is an area of the product (Firewall, Services, System). It is a
 *   sidebar row and a path segment; with pages, its link lands on the first.
 * - A page is a noun you would say in a sentence: "open NAT", "go to DNS". It
 *   is a path segment under its item with its own route and heading, and the
 *   sidebar lists it while you are inside the item.
 * - A tab is a facet of a page that shares its subject but not its screen:
 *   DHCP has IPv4, IPv6 and Leases. It lives in the URL hash, never nests,
 *   and never carries its own heading.
 *
 * Related forms short enough to scan together stay stacked on one page (the
 * three NAT tables); once a page would need a table of contents it has grown
 * into tabs. Live views sit as the last tab or page of the thing they report
 * on; Diagnostics is the home for tools with no configuration behind them.
 *
 * @typedef {{value: string, label: string}} Tab
 * @typedef {object} Page
 * @property {string} path segment under the item
 * @property {string} label sidebar row and page heading
 * @property {() => Promise<unknown>} view lazily imported component
 * @property {Tab[]} [tabs] facets the page renders with AppTabs, first is the default
 * @property {boolean} [needsConfig] the page edits the draft; without one it says so
 * @typedef {object} NavItem
 * @property {string} to path
 * @property {string} label sidebar row
 * @property {import('vue').Component} icon
 * @property {unknown} [view] component for an item that is one page
 * @property {Tab[]} [tabs] facets of a one-page item
 * @property {Page[]} [pages] pages of an item that has several
 * @property {string} [intro] a line under the heading of every page
 * @property {boolean} [needsConfig] default for the item's pages
 */

/** @type {NavItem[]} */
export const NAV = [
  { to: '/', label: 'Dashboard', icon: Activity, view: DashboardView },
  {
    to: '/interfaces',
    label: 'Interfaces',
    icon: Network,
    view: () => import('@/views/InterfacesView.vue'),
    tabs: [
      { value: 'interfaces', label: 'Interfaces' },
      { value: 'zones', label: 'Zones' },
    ],
  },
  {
    to: '/firewall',
    label: 'Firewall',
    icon: Shield,
    needsConfig: true,
    pages: [
      { path: 'rules', label: 'Rules', view: () => import('@/views/firewall/RulesPage.vue') },
      { path: 'aliases', label: 'Aliases', view: () => import('@/views/firewall/AliasesPage.vue') },
      {
        path: 'schedules',
        label: 'Schedules',
        view: () => import('@/views/firewall/SchedulesPage.vue'),
      },
      { path: 'nat', label: 'NAT', view: () => import('@/views/firewall/NatPage.vue') },
      {
        path: 'protection',
        label: 'Protection',
        view: () => import('@/views/firewall/ProtectionPage.vue'),
      },
      {
        path: 'shaping',
        label: 'Traffic shaping',
        view: () => import('@/views/firewall/ShapingPage.vue'),
        tabs: [
          { value: 'bandwidth', label: 'Bandwidth' },
          { value: 'priorities', label: 'Priorities' },
          { value: 'live', label: 'Live' },
        ],
      },
      { path: 'log', label: 'Log', view: () => import('@/views/firewall/LogPage.vue') },
    ],
  },
  { to: '/routing', label: 'Routing', icon: Route, view: () => import('@/views/RoutingView.vue') },
  {
    to: '/services',
    label: 'Services',
    icon: Server,
    needsConfig: true,
    pages: [
      {
        path: 'dhcp',
        label: 'DHCP',
        view: () => import('@/views/services/DhcpPage.vue'),
        tabs: [
          { value: 'v4', label: 'IPv4' },
          { value: 'v6', label: 'IPv6' },
          { value: 'leases', label: 'Leases' },
        ],
      },
      {
        path: 'dns',
        label: 'DNS',
        view: () => import('@/views/services/DnsPage.vue'),
        tabs: [
          { value: 'server', label: 'Server' },
          { value: 'lists', label: 'Block lists' },
          { value: 'exceptions', label: 'Exceptions' },
          { value: 'enforcement', label: 'Enforcement' },
        ],
      },
      {
        path: 'upnp',
        label: 'Port mapping',
        view: () => import('@/views/services/UpnpPage.vue'),
        tabs: [
          { value: 'service', label: 'Service' },
          { value: 'mappings', label: 'Mappings' },
        ],
      },
    ],
  },
  {
    to: '/vpn',
    label: 'VPN',
    icon: Lock,
    needsConfig: true,
    pages: [
      {
        path: 'wireguard',
        label: 'WireGuard',
        view: () => import('@/views/vpn/WireguardPage.vue'),
      },
    ],
  },
  {
    to: '/crons',
    label: 'Crons',
    icon: CalendarClock,
    view: () => import('@/views/CronsView.vue'),
  },
  {
    to: '/diagnostics',
    label: 'Diagnostics',
    icon: Stethoscope,
    pages: [
      {
        path: 'ping',
        label: 'Ping and traceroute',
        view: () => import('@/views/diagnostics/PingPage.vue'),
      },
      {
        path: 'connections',
        label: 'Connections',
        view: () => import('@/views/diagnostics/ConnectionsPage.vue'),
      },
      {
        path: 'neighbours',
        label: 'ARP and NDP',
        view: () => import('@/views/diagnostics/NeighboursPage.vue'),
      },
      {
        path: 'capture',
        label: 'Packet capture',
        view: () => import('@/views/diagnostics/CapturePage.vue'),
      },
      { path: 'logs', label: 'Logs', view: () => import('@/views/diagnostics/LogsPage.vue') },
    ],
  },
  {
    to: '/system',
    label: 'System',
    icon: Cog,
    pages: [
      {
        path: 'general',
        label: 'General',
        view: () => import('@/views/system/GeneralPage.vue'),
        needsConfig: true,
      },
      {
        path: 'accounts',
        label: 'Accounts',
        view: () => import('@/views/system/AccountsPage.vue'),
      },
      { path: 'host', label: 'Host', view: () => import('@/views/system/HostPage.vue') },
      { path: 'updates', label: 'Updates', view: () => import('@/views/system/UpdatesPage.vue') },
      {
        path: 'backup',
        label: 'Backup',
        view: () => import('@/views/system/BackupPage.vue'),
        needsConfig: true,
      },
      { path: 'ruleset', label: 'Ruleset', view: () => import('@/views/system/RulesetPage.vue') },
    ],
  },
]
