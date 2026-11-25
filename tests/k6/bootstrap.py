#!/usr/bin/env python3
"""Create synthetic users, group, grants, and separate target DB connections via public APIs."""
import http.cookiejar
import json
import os
import sys
import urllib.error
import urllib.request

base = os.environ['BASE_URL'].rstrip('/')
admin_user = os.environ['SQLWISE_ADMIN_USERNAME']
admin_password = os.environ['SQLWISE_ADMIN_PASSWORD']
run = os.environ['K6_RUN_PREFIX']

jar = http.cookiejar.CookieJar()
opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(jar))
csrf = ''


def request(method, path, body=None):
    global csrf
    headers = {'Accept': 'application/json'}
    data = None
    if body is not None:
        data = json.dumps(body).encode()
        headers['Content-Type'] = 'application/json'
        headers['X-CSRF-Token'] = csrf
    try:
        response = opener.open(urllib.request.Request(base + '/api/v1' + path, data=data, headers=headers, method=method), timeout=20)
        value = json.loads(response.read())
    except urllib.error.HTTPError as exc:
        raise RuntimeError(f'{method} {path} returned HTTP {exc.code}') from None
    if response.status >= 300:
        raise RuntimeError(f'{method} {path} returned HTTP {response.status}')
    return value.get('data', {})


challenge = request('GET', '/auth/login-challenge')
csrf = challenge['csrf_token']
login = request('POST', '/auth/login', {'username': admin_user, 'password': admin_password})
csrf = login['csrf_token']

editor = os.environ['K6_EDITOR_USERNAME']
leader = os.environ['K6_LEADER_USERNAME']
request('POST', '/admin/users', {'username': editor, 'password': os.environ['K6_EDITOR_PASSWORD']})
users = request('GET', '/admin/users')['users']
editor_id = next(user['id'] for user in users if user['username'] == editor)
request('POST', '/admin/users', {'username': leader, 'password': os.environ['K6_LEADER_PASSWORD']})
users = request('GET', '/admin/users')['users']
leader_id = next(user['id'] for user in users if user['username'] == leader)

group_name = 'k6-' + run
request('POST', '/admin/groups', {'name': group_name})
groups = request('GET', '/admin/groups')['groups']
group_id = next(group['id'] for group in groups if group['name'] == group_name)
for user_id in (editor_id, leader_id):
    request('POST', f'/admin/groups/{group_id}/members', {'user_id': user_id})
request('POST', f'/admin/groups/{group_id}/leader', {'user_id': leader_id})

count = int(os.environ.get('K6_DATABASE_COUNT', '16'))
ids = []
for index in range(count):
    result = request('POST', '/admin/databases', {
        'name': f'k6-{run}-{index}', 'dbms': 'postgresql',
        'host': os.environ.get('K6_TARGET_HOST', 'target'), 'port': os.environ.get('K6_TARGET_PORT', '5432'),
        'database': f"{os.environ['K6_TARGET_DATABASE_PREFIX']}_{index}",
        'username': os.environ.get('K6_TARGET_USERNAME', 'target_owner'),
        'password': os.environ.get('K6_TARGET_PASSWORD', 'target_owner_dev_only'),
        'tls': 'disable', 'sandbox': True,
    })
    db_id = result['form']['id']
    request('POST', f'/admin/groups/{group_id}/databases', {'database_id': db_id, 'permission': 'edit'})
    ids.append(db_id)

sys.stdout.write(','.join(ids) + '\n')
