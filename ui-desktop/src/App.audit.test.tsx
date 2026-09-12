import type { DeepLinkAction, PingResult, State } from "./api";
import { createElement } from 'react';
import { act, cleanup, fireEvent, screen, waitFor, within } from '@testing-library/react';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { App } from './App.tsx';
import { renderWithProviders } from './test/renderWithProviders.tsx';

const m = vi.hoisted(() => ({
  ready: true, coreError: null as string | null,
  checkNodes: vi.fn(), importSubscription: vi.fn(), refreshProfiles: vi.fn(),
  updateAvailable: null as string | null, updateConfirm: false, confirmUpdate: vi.fn(),
  connect: vi.fn(), disconnect: vi.fn(), onDeepLink: vi.fn(), deep: null as ((e: DeepLinkAction) => void) | null, pings: new Map<string, PingResult>(),
  profiles: [
    { id: 'p1', name: 'Profile A', source: 'manual', nodes: [{id:'n1',name:'Node A',protocol:'vless',server:'198.51.100.10',port:443}], updatedAt:'2026-01-01T00:00:00Z' },
    { id: 'p2', name: 'Profile B', source: 'manual', nodes: [{id:'n2',name:'Node B',protocol:'vless',server:'198.51.100.11',port:443}], updatedAt:'2026-01-01T00:00:00Z' },
  ]
}));
vi.mock('./state/useTenebra.ts', () => ({
  useTenebra: () => ({ ready: m.ready, coreError: m.coreError, state: {state:'idle',daemon_version:'0.5.11',crash_reports_asked:true} as State, profiles: m.profiles,
    traffic: {up:0,down:0,upRate:0,downRate:0},logs:[],attempts:null,pickProgress:null,
    connect:m.connect,disconnect:m.disconnect,refreshProfiles:m.refreshProfiles }),
}));
vi.mock('./api/index.ts', () => ({
  api: {checkNodes:m.checkNodes, importSubscription:m.importSubscription},
  onDeepLink: m.onDeepLink,
  onTrayConnect: vi.fn(async () => () => {}), onTrayShow: vi.fn(async () => () => {}),
  takeLaunchDeepLinks: vi.fn(async () => []),
}));
vi.mock('./lib/useNodePings.ts', () => ({
  useNodePings: () => ({results:m.pings,pinging:false,refresh:()=>{}}),
}));
vi.mock('./lib/useUpdateCheck.ts', () => ({
  useUpdateCheck: () => ({available:m.updateAvailable,stalled:false,confirming:m.updateConfirm,installing:false,deferred:false,progress:null,install:vi.fn(),dismiss:vi.fn(),cancelInstall:vi.fn(),confirmInstall:m.confirmUpdate}),
}));
beforeEach(() => {
  localStorage.clear();
  m.ready = true; m.coreError = null;
  m.deep = null;
  m.updateAvailable = null; m.updateConfirm = false;
  m.checkNodes.mockResolvedValue({best:"",results:[]});
  m.importSubscription.mockResolvedValue({name:"Imported profile"});
  m.refreshProfiles.mockResolvedValue(undefined);
  m.pings = new Map();
  m.onDeepLink.mockImplementation(async (handler) => { m.deep = handler; return () => {}; });
  m.connect.mockResolvedValue({state:'connecting'});
});

it('keeps failed ping unknown and permits a deliberate manual selection', async () => {
  m.pings.set('n1',{node:'n1',ok:false,rttMs:0});
  renderWithProviders(createElement(App));
  await screen.findAllByText('Node A');
  const row=screen.getByText('Node A',{selector:'.srv-node-code'}).closest('.srv-row')!;
  expect(row).toHaveAttribute('tabindex','0');
  expect(row).toHaveClass('is-dead');
  expect(document.querySelector('.cur-rtt')).toBeNull();
  expect(document.querySelectorAll('.cur-meta .ping-scale-bar.on.good')).toHaveLength(0);
});

it.each([['0', false, null], ['0', true, 'service lost'], ['1', false, null], ['1', true, 'service lost']] as const)(
  'blocks keyboard Connect as well as the button in mode %s with ready=%s error=%s', async (mode, ready, error) => {
    localStorage.setItem('tenebra.simpleMode', mode);
    m.ready = ready; m.coreError = error;
    renderWithProviders(createElement(App));
    await screen.findAllByText('Node A');
    expect(screen.getByRole('button', {name: /^(▶\s*)?Connect$/})).toBeDisabled();
    await act(async () => { fireEvent.keyDown(document.body, {key:' ',code:'Space'}); });
    expect(m.checkNodes).not.toHaveBeenCalled();
    expect(m.connect).not.toHaveBeenCalled();
  },
);

it.each(['0','1'])('blocks keyboard Connect for a saved subscription without nodes in mode %s', async (mode) => {
  localStorage.setItem('tenebra.simpleMode',mode);
  const saved = m.profiles;
  m.profiles = saved.map(p => ({...p,nodes:[]}));
  try {
    renderWithProviders(createElement(App));
    await act(async () => {});
    expect(screen.getByRole('button',{name:/^(▶\s*)?Connect$/})).toBeDisabled();
    await act(async () => { fireEvent.keyDown(document.body,{key:' ',code:'Space'}); });
    expect(m.checkNodes).not.toHaveBeenCalled();
    expect(m.connect).not.toHaveBeenCalled();
  } finally { m.profiles = saved; }
});

it('waits for the service before asking a new full-mode user to import', async () => {
  const saved = m.profiles;
  m.profiles = []; m.ready = false;
  try {
    renderWithProviders(createElement(App));
    expect(screen.getByRole('heading',{name:'Starting Tenebra…'})).toBeInTheDocument();
    expect(screen.queryByRole('textbox',{name:/subscription link/i})).toBeNull();
  } finally { m.profiles = saved; }
});
afterEach(() => cleanup());

it.each(['0', '1'])('keeps first launch focused on a single subscription task in mode %s', async (mode) => {
  localStorage.setItem('tenebra.simpleMode', mode);
  const saved = m.profiles;
  m.profiles = [];
  try {
    renderWithProviders(createElement(App));
    await act(async () => {});
    expect(screen.getByRole('textbox', {name:/subscription link/i})).toBeInTheDocument();
    expect(document.querySelector('.srv-add')).toBeNull();
    expect(document.querySelector('.connect-btn')).toBeNull();
    expect(document.querySelector('.simple-btn')).toBeNull();
  } finally { m.profiles = saved; }
});

it('offers the same TUN conflict confirmation from a profile card', async () => {
  m.connect.mockRejectedValue(new Error('another VPN owns the default route'));
  renderWithProviders(createElement(App));
  await screen.findAllByText('Node A');
  fireEvent.click(document.querySelector('.srv-add')!);
  const card = screen.getByRole('heading', {name:'Profile A'}).closest('li')!;
  fireEvent.click(within(card).getByRole('button', {name:/nodes/i}));
  fireEvent.click(within(card).getByRole('button', {name:'Connect'}));
  const prompt = await screen.findByRole('alertdialog');
  await act(async () => fireEvent.click(within(prompt).getByRole('button', {name:/cancel/i})));
  expect(m.connect).toHaveBeenCalledTimes(1);
});

it('clears profile A node when selecting profile B from its card', async () => {
  renderWithProviders(createElement(App));
  await screen.findAllByText('Node A');
  fireEvent.click(screen.getByText('Node A', {selector:'.srv-node-code'}));
  const add = document.querySelector('.srv-add');
  if (add) fireEvent.click(add); else fireEvent.click(screen.getByRole('button', {name:/subscription/i}));
  const cardB = screen.getByRole('heading', {name:'Profile B'}).closest('li')!;
  fireEvent.click(within(cardB).getByRole('button', {name:/set active/i}));
  fireEvent.click(document.querySelector('.overlay-close')!);
  fireEvent.click(document.querySelector('.connect-btn')!);
  await waitFor(() => expect(m.connect).toHaveBeenCalledTimes(1));
  expect(m.connect.mock.calls[0].slice(0,2)).toEqual(['p2',undefined]);
});

it('shows and cancels a connect deep link in simple mode', async () => {
  localStorage.setItem('tenebra.simpleMode','1');
  renderWithProviders(createElement(App));
  await waitFor(() => expect(m.deep).toBeTypeOf('function'));
  act(() => m.deep!({action:'connect',profile:'p1'}));
  expect(screen.getByRole('alertdialog')).toBeInTheDocument();
  expect(m.connect).not.toHaveBeenCalled();
  fireEvent.click(within(screen.getByRole('alertdialog')).getByRole('button', {name: /not now/i}));
  expect(m.connect).not.toHaveBeenCalled();
});



it('imports a received subscription link without leaving simple mode', async () => {
  localStorage.setItem('tenebra.simpleMode','1');
  renderWithProviders(createElement(App));
  await waitFor(() => expect(m.deep).toBeTypeOf('function'));
  act(() => m.deep!({action:'import',url:'https://example.invalid/sub'}));
  const modal = await screen.findByRole('dialog', {name:'Import'});
  expect(within(modal).getByDisplayValue('https://example.invalid/sub')).toBeInTheDocument();
  fireEvent.change(within(modal).getByRole('textbox', {name:'Name'}), {target:{value:'Imported profile'}});
  fireEvent.click(within(modal).getByRole('button', {name:'Import'}));
  await waitFor(() => expect(m.refreshProfiles).toHaveBeenCalledTimes(1));
  expect(m.importSubscription).toHaveBeenCalledWith('https://example.invalid/sub','Imported profile');
  expect(document.querySelector('.app--simple')).toBeInTheDocument();
});

it('shows the update confirmation and acts only on its explicit approval in simple mode', async () => {
  localStorage.setItem('tenebra.simpleMode','1');
  m.updateAvailable = '9.9.9'; m.updateConfirm = true;
  renderWithProviders(createElement(App));
  const modal = await screen.findByRole('alertdialog');
  expect(document.querySelector('.update-banner')).toBeInTheDocument();
  expect(m.confirmUpdate).not.toHaveBeenCalled();
  fireEvent.click(within(modal).getByRole('button', {name:/install now/i}));
  expect(m.confirmUpdate).toHaveBeenCalledTimes(1);
});

it('distinguishes a failed prober from no usable node and still tries the connection', async () => {
  m.checkNodes.mockRejectedValue(new Error('probe process unavailable'));
  renderWithProviders(createElement(App));
  await screen.findAllByText('Node A');
  fireEvent.click(document.querySelector('.connect-btn')!);
  await waitFor(() => expect(m.connect).toHaveBeenCalledTimes(1));
  expect(screen.getByText(/The node check could not run/)).toBeInTheDocument();
  expect(screen.queryByText(/No node carried traffic/)).toBeNull();
});
