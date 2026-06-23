// Cascade Fleet — Übersicht desktop widget. Shows all linked accounts + live quota.
// Reads ~/.cascade/accounts.json + ~/.cascade/quota-store.json. Refreshes every 30s.
export const refreshFrequency = 30000;

export const command = `python3 -c "
import json,os
h=os.path.expanduser
acc=json.load(open(h('~/.cascade/accounts.json'))) if os.path.exists(h('~/.cascade/accounts.json')) else {}
q=json.load(open(h('~/.cascade/quota-store.json'))) if os.path.exists(h('~/.cascade/quota-store.json')) else {}
print(json.dumps({'accounts':acc.get('accounts',[]),'quota':q}))
" 2>/dev/null || echo '{}'`;

export const className = { top: 80, left: 40, fontFamily: '-apple-system, SF Pro Text, sans-serif', color: '#e6e6e6' };

export const render = ({ output }) => {
  let d = {}; try { d = JSON.parse(output || '{}'); } catch (e) {}
  const accounts = d.accounts || [];
  const fam = { Claude:'🟣', Openai:'🟢', Google:'🔵', Opencode:'🟠', Gfp:'⚡' };
  return (
    <div style={{ width:340, padding:'14px 16px', background:'rgba(20,20,28,0.82)', borderRadius:12,
      backdropFilter:'blur(12px)', boxShadow:'0 8px 30px rgba(0,0,0,0.45)', fontSize:12.5 }}>
      <div style={{ fontWeight:700, fontSize:14, marginBottom:8, letterSpacing:0.3 }}>⛰  Cascade Fleet</div>
      {accounts.length === 0 && <div style={{opacity:0.6}}>No accounts — run <code>cascade accounts detect</code></div>}
      {accounts.map(a => (
        <div key={a.id} style={{ display:'flex', alignItems:'center', justifyContent:'space-between',
          padding:'4px 0', borderBottom:'1px solid rgba(255,255,255,0.06)' }}>
          <span>{fam[a.family]||'•'} <b>{a.id}</b>
            <span style={{opacity:0.55, marginLeft:6}}>{(a.access_methods||[a.access]||[]).join?.(',')||''}</span>
          </span>
          <span style={{opacity:0.8, fontVariantNumeric:'tabular-nums'}}>
            {a.family==='Gfp' ? `${a.key_count||a.keys||0} keys` :
             (a.cli_available ? '✓' : '—')}
            {a.role==='PrimaryT0' && <span style={{color:'#7aa2ff', marginLeft:6}}>T0</span>}
          </span>
        </div>
      ))}
      <div style={{opacity:0.45, fontSize:10.5, marginTop:8}}>updated every 30s · ~/.cascade/accounts.json</div>
    </div>
  );
};
