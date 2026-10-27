#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
export HELM="${HELM:-helm}"
for values in example tls mtls full; do
  "$HELM" lint --strict charts/herzi -f "charts/herzi/ci/$values-values.yaml"
done
python3 - <<'PY'
import copy,json,os,subprocess,tempfile
from pathlib import Path
import yaml
chart=Path('charts/herzi')
helm=os.environ['HELM']
base=yaml.safe_load((chart/'values.yaml').read_text())
example=yaml.safe_load((chart/'ci/example-values.yaml').read_text())
namespace='chart-test'
def run(args):
    return subprocess.run([helm,*args],text=True,capture_output=True)
def render(file='example',extra=None,sets=()):
    args=['template','herzi',str(chart),'-n',namespace,'-f',str(chart/f'ci/{file}-values.yaml')]
    if extra is not None:
        with tempfile.NamedTemporaryFile(mode='w',suffix='.yaml') as f:
            yaml.safe_dump(extra,f); f.flush()
            result=run([*args,'-f',f.name,*sets])
    else: result=run([*args,*sets])
    assert result.returncode==0,result.stderr
    return [d for d in yaml.safe_load_all(result.stdout) if d]
def get(docs,kind,suffix=''):
    return next(d for d in docs if d['kind']==kind and d['metadata']['name'].endswith(suffix))
def pod(docs):return get(docs,'Deployment')['spec']['template']
def checksum(docs):return pod(docs)['metadata']['annotations']['checksum/startup-config']
def merged(a,b):
    result=copy.deepcopy(a)
    for k,v in b.items(): result[k]=merged(result.get(k,{}),v) if isinstance(v,dict) else v
    return result
rules=yaml.safe_load((chart/'files/rbac-rules.yaml').read_text())
for mode in ['example','tls','mtls','full']:
    docs=render(mode)
    # Generated rules are shipped exactly, and manager access stays read/patch only.
    actual=get(docs,'ClusterRole','-manager')['rules']
    assert actual==rules,'manager RBAC differs from generated rules'
    for rule in actual:
        assert not set(rule['verbs']) & {'delete','deletecollection','update','create'},rule
        assert '*' not in json.dumps(rule),'wildcard manager RBAC'
    # Fixed hardening, non-overlapping rollouts and the AppVersion image fallback.
    template=pod(docs);spec=template['spec'];container=spec['containers'][0]
    sc=spec['securityContext'];csc=container['securityContext']
    assert sc['runAsNonRoot'] is True and 'runAsUser' not in sc and 'runAsGroup' not in sc
    assert sc['seccompProfile']['type']=='RuntimeDefault'
    assert csc['readOnlyRootFilesystem'] is True and csc['allowPrivilegeEscalation'] is False
    assert csc['capabilities']['drop']==['ALL']
    strategy=get(docs,'Deployment')['spec']['strategy']
    assert strategy=={'type':'RollingUpdate','rollingUpdate':{'maxSurge':0,'maxUnavailable':1}}
    version=yaml.safe_load((chart/'Chart.yaml').read_text())['appVersion']
    assert container['image']==example['image']['repository']+':'+version
    # Config is a directory mount, and the exact merged values are valid YAML.
    mount=next(m for m in container['volumeMounts'] if m['name']=='config')
    assert mount=={'name':'config','mountPath':'/etc/herzi','readOnly':True}
    cm=get(docs,'ConfigMap','-config');cfg=yaml.safe_load(cm['data']['config.yaml'])
    supplied=yaml.safe_load((chart/f'ci/{mode}-values.yaml').read_text())
    expected=merged(base,supplied)['config']
    assert cfg==expected and cfg['defaults']==expected['defaults'] and cfg['pools']==expected['pools']
    # Standard labels on every object; all namespaced objects use the release namespace.
    for obj in docs:
        labels=obj['metadata']['labels']
        assert labels['app.kubernetes.io/name']=='herzi'
        assert labels['app.kubernetes.io/instance']=='herzi'
        assert labels['app.kubernetes.io/version']==version
        assert labels['app.kubernetes.io/managed-by']=='Helm'
        assert labels['helm.sh/chart']=='herzi-0.1.0'
        if obj['kind'] not in ['ClusterRole','ClusterRoleBinding']:
            assert obj['metadata']['namespace']==namespace,obj
    # TLS and mTLS env/volume wiring, including immutable external Secret references.
    env={e['name']:e for e in container['env']};volumes={v['name']:v for v in spec['volumes']}
    assert env['GOMEMLIMIT']['value']=='400MiB'
    if mode=='example':
        assert 'HERZI_TEMPORAL_TLS_CA_FILE' not in env and 'temporal-tls' not in volumes
        assert 'HERZI_API_BEARER_TOKEN' not in env
    else:
        assert env['HERZI_TEMPORAL_TLS_CA_FILE']['value']=='/etc/herzi-temporal/ca.crt'
        assert volumes['temporal-tls']['secret']['secretName']=='herzi-temporal-tls'
        assert volumes['metrics-tls']['secret']['secretName']=='herzi-metrics-tls'
        assert '--metrics-cert-path=/etc/herzi-metrics' in container['args']
        for name in ['temporal-tls','metrics-tls']:
            assert volumes[name]['secret']['defaultMode']==0o444
            assert next(m for m in container['volumeMounts'] if m['name']==name)['readOnly'] is True
    if mode in ['mtls','full']:
        assert env['HERZI_TEMPORAL_TLS_CERT_FILE']['value']=='/etc/herzi-temporal/tls.crt'
        assert env['HERZI_TEMPORAL_TLS_KEY_FILE']['value']=='/etc/herzi-temporal/tls.key'
    else:
        assert 'HERZI_TEMPORAL_TLS_CERT_FILE' not in env and 'HERZI_TEMPORAL_TLS_KEY_FILE' not in env
    for kind in ['ServiceMonitor','PrometheusRule','NetworkPolicy']:
        assert any(d['kind']==kind for d in docs)==(mode=='full'),(mode,kind)
    reader_bindings=[d for d in docs if d['kind']=='ClusterRoleBinding' and d['metadata']['name'].endswith('-metrics-reader')]
    assert bool(reader_bindings)==(mode=='full')
    assert get(docs,'Service')['spec']['ports'][0]['name']=='https'
    leader=get(docs,'Role')['rules']
    assert leader==[{'apiGroups':['coordination.k8s.io'],'resources':['leases'],'verbs':['get','list','watch','create','update','patch','delete']},{'apiGroups':[''],'resources':['events'],'verbs':['create','patch']}]
    if mode=='full':
        assert '{{ $labels.pool }}' in yaml.safe_dump(get(docs,'PrometheusRule')['spec'])
        assert get(docs,'PrometheusRule')['spec']==yaml.safe_load((chart/'files/alerts.rules.yaml').read_text())
        assert reader_bindings[0]['subjects']==supplied['rbac']['metricsReader']['subjects']
        for key,secret in [('HERZI_API_BEARER_TOKEN','herzi-api'),('HERZI_TEMPORAL_API_KEY','herzi-temporal-api')]:
            ref=env[key]['valueFrom']['secretKeyRef'];assert ref['name']==secret and 'optional' not in ref
        monitor=get(docs,'ServiceMonitor')['spec']['endpoints'][0]
        assert monitor['tlsConfig']['ca']['secret']['name']=='herzi-metrics-tls'
        assert monitor['bearerTokenSecret']==supplied['serviceMonitor']['bearerTokenSecret']
        policy=get(docs,'NetworkPolicy')['spec']
        assert policy['egress'][-1]['ports']==[{'port':5353,'protocol':'UDP'},{'port':5353,'protocol':'TCP'}]
        assert [e['to'][0]['ipBlock']['cidr'] for e in policy['egress'][:-1]]==['192.0.2.10/32','192.0.2.20/32','192.0.2.30/32']
# Live changes preserve the startup checksum; every mutable startup setting changes it.
original=checksum(render())
for extra in [{'config':{'pools':{'32-128-zone-1':{'minFree':6}}}},{'config':{'interval':'3m','reconcileTimeout':'50s','ambiguousWait':'6m','defaults':{'highPct':20}}}]:
    assert checksum(render(extra=extra))==original,'live config caused a restart'
for key,value in {'hubName':'ocp4-other','agentNamespace':'other','installURL':'https://other.invalid/install','stuckAfter':'3h','temporalAddress':'other:7233','temporalNamespace':'other','activityQueue':'other-a','workflowQueue':'other-w','segmentQueue':'other-s'}.items():
    assert checksum(render(extra={'config':{key:value}}))!=original,key+' did not cause a restart'
# The checksum must be precisely the JSON hash of the startup-bound keys.
import hashlib
startup={k:merged(base,example)['config'][k] for k in ['hubName','agentNamespace','installURL','maxSlots','stuckAfter','temporalAddress','temporalNamespace','activityQueue','workflowQueue','segmentQueue']}
assert original==hashlib.sha256(json.dumps(startup,sort_keys=True,separators=(',',':')).encode()).hexdigest()
# Independent toggles, external service account and Kubernetes DNS selection.
docs=render(extra={'rbac':{'create':False},'serviceAccount':{'create':False,'name':'existing'},'metrics':{'enabled':False}})
assert not any(d['kind'] in ['Role','RoleBinding','ClusterRole','ClusterRoleBinding','ServiceAccount','Service'] for d in docs)
assert pod(docs)['spec']['serviceAccountName']=='existing'
policy=get(render('full',extra={'networkPolicy':{'dns':'kubernetes'}}),'NetworkPolicy')['spec']
assert policy['egress'][-1]['ports'][0]['port']==53
assert policy['egress'][-1]['to'][0]['podSelector']['matchLabels']=={'k8s-app':'kube-dns'}
# Empty installation fails with all installer-required keys named.
result=run(['template','herzi',str(chart)])
assert result.returncode!=0
for key in ['hubName','installURL','temporalAddress','repository']:assert key in result.stderr,result.stderr
# Invalid config fails early, including explicit migration guidance for removed keys.
def invalid(extra,words):
    with tempfile.NamedTemporaryFile(mode='w',suffix='.yaml') as f:
        yaml.safe_dump(extra,f);f.flush()
        for command in ['template','lint']:
            args=[command,'herzi',str(chart)] if command=='template' else [command,'--strict',str(chart)]
            result=run([*args,'-f',str(chart/'ci/example-values.yaml'),'-f',f.name])
            assert result.returncode!=0,(command,extra)
            for word in words:assert word in result.stderr+result.stdout,(extra,result.stderr,result.stdout)
for key in ['lowPct','minBand']:
    for value in [0,None]:
        for location in [{'defaults':{key:value}},{'pools':{'example':{key:value}}}]:invalid({'config':location},[key,'removed'])
invalid({'config':{'maxSlots':50}},['maxSlots','100'])
invalid({'config':{'defaults':{'highPct':1001}}},['highPct'])
invalid({'config':{'pools':{'example':{'minFree':100001}}}},['minFree'])
invalid({'config':{'defaults':{'typo':1}}},['typo'])
invalid({'config':{'interval':'20minutes'}},['interval'])
invalid({'config':{'defaults':{'paused':'true'}}},['paused'])
invalid({'temporal':{'mtls':{'enabled':True}}},['existingSecret'])
print('Chart assertions passed: RBAC, hardening, image, config, checksum, TLS, monitoring, network, labels, namespace, schema')
PY
