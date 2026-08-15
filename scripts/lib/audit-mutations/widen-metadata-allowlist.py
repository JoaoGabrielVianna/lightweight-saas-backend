src = re.sub(
    r'("project_credential\.created":\s*\{)',
    r'\1\n\t\t"token": true,',
    src, count=1)
